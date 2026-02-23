package main

import (
	"context"
	"io"
	"time"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
)

type ConversationServer struct {
	assistantpb.UnimplementedConversationOrchestratorServer

	logger        *logrus.Logger
	stateStore    StateStore
	classifier    assistantpb.IntentClassifierClient
	extractor     assistantpb.EntityExtractorClient
	weatherClient assistantpb.WeatherServiceClient
}

func NewConversationServer(
	logger *logrus.Logger,
	stateStore StateStore,
	classifier assistantpb.IntentClassifierClient,
	extractor assistantpb.EntityExtractorClient,
	weather assistantpb.WeatherServiceClient,
) *ConversationServer {
	return &ConversationServer{
		logger:        logger,
		stateStore:    stateStore,
		classifier:    classifier,
		extractor:     extractor,
		weatherClient: weather,
	}
}

func (s *ConversationServer) Converse(stream assistantpb.ConversationOrchestrator_ConverseServer) error {
	ctx := stream.Context()
	tr := otel.Tracer("orchestrator.conversation")

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		ctxMsg, span := tr.Start(ctx, "dialog.turn")
		span.SetAttributes(
			attribute.String("assistant.session_id", msg.GetSessionId()),
			attribute.String("assistant.user_id", msg.GetUserId()),
		)

		resp, procErr := s.handleClientMessage(ctxMsg, msg)
		if procErr != nil {
			span.RecordError(procErr)
			span.SetStatus(codes.Error, procErr.Error())
		}

		if sendErr := stream.Send(resp); sendErr != nil {
			span.RecordError(sendErr)
			span.SetStatus(codes.Error, sendErr.Error())
			span.End()
			return sendErr
		}

		span.End()
	}
}

func (s *ConversationServer) GetHistory(ctx context.Context, req *assistantpb.HistoryRequest) (*assistantpb.HistoryResponse, error) {
	st, err := s.stateStore.GetSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, err
	}
	if st == nil {
		return &assistantpb.HistoryResponse{}, nil
	}
	return &assistantpb.HistoryResponse{
		Messages: st.History,
	}, nil
}

func (s *ConversationServer) handleClientMessage(ctx context.Context, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	logger := s.logger.WithFields(logrus.Fields{
		"session_id": msg.GetSessionId(),
		"user_id":    msg.GetUserId(),
	})
	logger.Info("processing client message")

	state, err := s.stateStore.GetSession(ctx, msg.GetSessionId())
	if err != nil {
		return s.errorMessage(msg.GetSessionId(), "internal", "failed to load session"), err
	}
	if state == nil {
		state = &SessionState{
			SessionID: msg.GetSessionId(),
			UserID:    msg.GetUserId(),
			Status:    StatusNew,
			Entities:  make(map[string]*assistantpb.EntityValue),
			Context:   make(map[string]string),
			History:   []*assistantpb.ClientMessage{},
		}
	}

	state.History = append(state.History, msg)
	state.UpdatedAt = time.Now().UTC()

	switch state.Status {
	case StatusNew, StatusCompleted:
		return s.processNewOrCompleted(ctx, state, msg)
	case StatusProcessing, StatusCollecting, StatusConfirming:
		return s.processOngoing(ctx, state, msg)
	default:
		return s.errorMessage(msg.GetSessionId(), "invalid_state", "unknown dialog state"), nil
	}
}

func (s *ConversationServer) processNewOrCompleted(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	// For MVP: always call NLP services and try to detect intent and entities.
	state.Status = StatusProcessing

	classResp, err := s.classifier.Classify(ctx, &assistantpb.ClassifyRequest{
		Utterance:       msg.GetText(),
		PreviousIntents: nil,
	})
	if err != nil {
		return s.errorMessage(state.SessionID, "classifier_unavailable", "classifier service error, falling back"), err
	}

	state.CurrentIntent = classResp.GetIntentName()

	extractResp, err := s.extractor.Extract(ctx, &assistantpb.ExtractRequest{
		Utterance:        msg.GetText(),
		Intent:           classResp.GetIntentName(),
		ExistingEntities: nil,
	})
	if err != nil {
		return s.errorMessage(state.SessionID, "extractor_unavailable", "extractor service error"), err
	}

	for k, v := range extractResp.Entities {
		state.Entities[k] = v
	}

	// For Phase 1 we only support the weather intent.
	var serverMsg *assistantpb.ServerMessage
	if classResp.GetIntentName() == "get_weather" {
		serverMsg, err = s.handleWeatherIntent(ctx, state, msg)
	} else {
		serverMsg = &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Text{
				Text: &assistantpb.TextResponse{
					Text: "Пока я умею только рассказывать о погоде.",
				},
			},
		}
	}

	if saveErr := s.stateStore.SaveSession(ctx, state); saveErr != nil && err == nil {
		err = saveErr
	}
	return serverMsg, err
}

func (s *ConversationServer) processOngoing(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	// For MVP: treat every incoming message as a new turn.
	state.Status = StatusProcessing
	return s.processNewOrCompleted(ctx, state, msg)
}

func (s *ConversationServer) handleWeatherIntent(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	location := ""
	if v, ok := state.Entities["location"]; ok {
		if strVal := v.GetStringValue(); strVal != "" {
			location = strVal
		}
	}
	if location == "" {
		// Simple clarification question without full COLLECTING logic.
		state.Status = StatusCollecting
		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Confirmation{
				Confirmation: &assistantpb.ConfirmationRequest{
					Question: "For which location do you need the weather?",
				},
			},
		}, nil
	}

	now := time.Now().UTC()
	weatherResp, err := s.weatherClient.GetWeather(ctx, &assistantpb.WeatherRequest{
		UserId:   state.UserID,
		Location: location,
		Date:     timestamppb.New(now),
	})
	if err != nil {
		return s.errorMessage(state.SessionID, "weather_unavailable", "weather service error"), err
	}

	state.Status = StatusCompleted

	text := weatherResp.GetSummary()
	if text == "" {
		text = "Weather for " + location
	}

	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload: &assistantpb.ServerMessage_Text{
			Text: &assistantpb.TextResponse{
				Text: text,
			},
		},
	}, nil
}

func (s *ConversationServer) errorMessage(sessionID, code, msg string) *assistantpb.ServerMessage {
	return &assistantpb.ServerMessage{
		SessionId: sessionID,
		Payload: &assistantpb.ServerMessage_Error{
			Error: &assistantpb.ErrorResponse{
				Code:    code,
				Message: msg,
			},
		},
	}
}

