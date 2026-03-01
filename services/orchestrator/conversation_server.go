package main

import (
	"context"
	"io"
	"strings"
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

	logger         *logrus.Logger
	stateStore     StateStore
	classifier     assistantpb.IntentClassifierClient
	extractor      assistantpb.EntityExtractorClient
	weatherClient  assistantpb.WeatherServiceClient
	reminderClient assistantpb.ReminderServiceClient
}

func NewConversationServer(
	logger *logrus.Logger,
	stateStore StateStore,
	classifier assistantpb.IntentClassifierClient,
	extractor assistantpb.EntityExtractorClient,
	weather assistantpb.WeatherServiceClient,
	reminder assistantpb.ReminderServiceClient,
) *ConversationServer {
	return &ConversationServer{
		logger:         logger,
		stateStore:     stateStore,
		classifier:     classifier,
		extractor:      extractor,
		weatherClient:  weather,
		reminderClient: reminder,
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
	case StatusProcessing:
		return s.processNewOrCompleted(ctx, state, msg)
	case StatusCollecting:
		return s.processCollecting(ctx, state, msg)
	case StatusConfirming:
		return s.processConfirming(ctx, state, msg)
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

	var serverMsg *assistantpb.ServerMessage
	switch classResp.GetIntentName() {
	case "get_weather":
		serverMsg, err = s.handleWeatherIntent(ctx, state, msg)
	case "create_reminder":
		serverMsg, err = s.handleReminderIntent(ctx, state, msg)
	default:
		serverMsg = &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Text{
				Text: &assistantpb.TextResponse{
					Text: "For now I can answer weather and reminder questions.",
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
	// Deprecated path, kept for backward compatibility. New logic branches on explicit states.
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
		// Simple clarification question; switch FSM into COLLECTING state.
		state.MissingEntities = []string{"location"}
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

func (s *ConversationServer) handleReminderIntent(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	text := ""
	if v, ok := state.Entities["text"]; ok {
		if strVal := v.GetStringValue(); strVal != "" {
			text = strVal
		}
	}
	if text == "" {
		// Fallback: use the full user utterance as reminder text.
		text = msg.GetText()
	}

	req := &assistantpb.CreateReminderRequest{
		UserId: state.UserID,
		Text:   text,
	}

	resp, err := s.reminderClient.CreateReminder(ctx, req)
	if err != nil {
		return s.errorMessage(state.SessionID, "reminder_unavailable", "reminder service error"), err
	}

	state.Status = StatusCompleted

	ack := "Created reminder: \"" + resp.GetReminder().GetText() + "\" at " + resp.GetReminder().GetDatetime().AsTime().Format(time.RFC3339)

	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload: &assistantpb.ServerMessage_Text{
			Text: &assistantpb.TextResponse{
				Text: ack,
			},
		},
	}, nil
}

// processCollecting handles dialog turns when we are missing some entities for the current intent.
// For Phase 1 it only collects the "location" entity for the weather intent.
func (s *ConversationServer) processCollecting(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	// Re-run entity extraction using the latest user utterance and merge entities.
	extractResp, err := s.extractor.Extract(ctx, &assistantpb.ExtractRequest{
		Utterance:        msg.GetText(),
		Intent:           state.CurrentIntent,
		ExistingEntities: map[string]string{},
	})
	if err != nil {
		return s.errorMessage(state.SessionID, "extractor_unavailable", "extractor service error during entity collection"), err
	}

	for k, v := range extractResp.Entities {
		state.Entities[k] = v
	}

	// Check whether we now have a location.
	location := ""
	if v, ok := state.Entities["location"]; ok {
		if strVal := v.GetStringValue(); strVal != "" {
			location = strVal
		}
	}

	if location == "" {
		// Still missing location – stay in COLLECTING and ask again.
		state.Status = StatusCollecting
		state.MissingEntities = []string{"location"}
		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Confirmation{
				Confirmation: &assistantpb.ConfirmationRequest{
					Question: "I still do not see the location. For which location do you need the weather?",
				},
			},
		}, s.stateStore.SaveSession(ctx, state)
	}

	// We have all required entities for weather – move to CONFIRMING.
	state.Status = StatusConfirming
	state.MissingEntities = nil

	question := "You want to know the weather in " + location + ". Is that correct? (yes/no)"
	if err := s.stateStore.SaveSession(ctx, state); err != nil {
		return s.errorMessage(state.SessionID, "internal", "failed to save session state"), err
	}

	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload: &assistantpb.ServerMessage_Confirmation{
			Confirmation: &assistantpb.ConfirmationRequest{
				Question: question,
			},
		},
	}, nil
}

// processConfirming handles yes/no answers when the system asks for confirmation.
func (s *ConversationServer) processConfirming(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	answer := strings.TrimSpace(strings.ToLower(msg.GetText()))

	isYes := answer == "yes" || answer == "y" || answer == "yeah" || answer == "ok" || answer == "okay" || answer == "sure"
	isNo := answer == "no" || answer == "n" || answer == "nope"

	if !isYes && !isNo {
		// Ask the user to answer explicitly with yes or no.
		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Confirmation{
				Confirmation: &assistantpb.ConfirmationRequest{
					Question: "Please answer with \"yes\" or \"no\".",
				},
			},
		}, nil
	}

	if isNo {
		// Reset collected entities and go back to COLLECTING.
		delete(state.Entities, "location")
		state.Status = StatusCollecting
		state.MissingEntities = []string{"location"}

		if err := s.stateStore.SaveSession(ctx, state); err != nil {
			return s.errorMessage(state.SessionID, "internal", "failed to save session state"), err
		}

		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Confirmation{
				Confirmation: &assistantpb.ConfirmationRequest{
					Question: "Okay, then for which location do you need the weather?",
				},
			},
		}, nil
	}

	// isYes: execute the intent using the already collected entities.
	resp, err := s.handleWeatherIntent(ctx, state, msg)
	if err != nil {
		return resp, err
	}

	// handleWeatherIntent already sets the status and saves the session.
	return resp, nil
}


