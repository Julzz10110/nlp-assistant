package main

import (
	"context"
	"fmt"
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

const minIntentConfidence = 0.55

var supportedIntents = map[string]struct{}{
	"get_weather":     {},
	"create_reminder": {},
	"book_table":      {},
}

type ConversationServer struct {
	assistantpb.UnimplementedConversationOrchestratorServer

	logger         *logrus.Logger
	stateStore     StateStore
	classifier     assistantpb.IntentClassifierClient
	extractor      assistantpb.EntityExtractorClient
	weatherClient  assistantpb.WeatherServiceClient
	reminderClient assistantpb.ReminderServiceClient
	bookingClient  assistantpb.BookingServiceClient
}

func NewConversationServer(
	logger *logrus.Logger,
	stateStore StateStore,
	classifier assistantpb.IntentClassifierClient,
	extractor assistantpb.EntityExtractorClient,
	weather assistantpb.WeatherServiceClient,
	reminder assistantpb.ReminderServiceClient,
	booking assistantpb.BookingServiceClient,
) *ConversationServer {
	return &ConversationServer{
		logger:         logger,
		stateStore:     stateStore,
		classifier:     classifier,
		extractor:      extractor,
		weatherClient:  weather,
		reminderClient: reminder,
		bookingClient:  booking,
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
	startedAt := time.Now()
	logger := s.logger.WithFields(logrus.Fields{
		"session_id": msg.GetSessionId(),
		"user_id":    msg.GetUserId(),
	})
	logger.Info("processing client message")

	state, err := s.stateStore.GetSession(ctx, msg.GetSessionId())
	if err != nil {
		return s.errorMessage(msg.GetSessionId(), "internal", "failed to load session"), err
	}
	isNewSession := state == nil
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

	var (
		resp    *assistantpb.ServerMessage
		handleErr error
	)
	switch state.Status {
	case StatusNew, StatusCompleted:
		resp, handleErr = s.processNewOrCompleted(ctx, state, msg)
	case StatusProcessing:
		resp, handleErr = s.processNewOrCompleted(ctx, state, msg)
	case StatusCollecting:
		resp, handleErr = s.processCollecting(ctx, state, msg)
	case StatusConfirming:
		resp, handleErr = s.processConfirming(ctx, state, msg)
	default:
		resp, handleErr = s.errorMessage(msg.GetSessionId(), "invalid_state", "unknown dialog state"), nil
	}
	status := "ok"
	if handleErr != nil {
		status = "error"
	}
	observeRequest(state.CurrentIntent, status, startedAt)
	if isNewSession {
		assistantActiveSessions.Inc()
	}
	return resp, handleErr
}

func (s *ConversationServer) processNewOrCompleted(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	// For MVP: always call NLP services and try to detect intent and entities.
	state.Status = StatusProcessing

	var previousIntents []string
	if state.CurrentIntent != "" {
		previousIntents = append(previousIntents, state.CurrentIntent)
	}
	classResp, err := s.classifier.Classify(ctx, &assistantpb.ClassifyRequest{
		Utterance:       msg.GetText(),
		PreviousIntents: previousIntents,
	})
	if err != nil {
		return s.errorMessage(state.SessionID, "classifier_unavailable", "classifier service error, falling back"), err
	}

	intentName := classResp.GetIntentName()
	confidence := classResp.GetConfidence()
	if _, ok := supportedIntents[intentName]; !ok || confidence < minIntentConfidence {
		assistantFallbackTotal.WithLabelValues("low_confidence_or_unsupported_intent").Inc()
		state.Status = StatusCompleted
		if saveErr := s.stateStore.SaveSession(ctx, state); saveErr != nil {
			return s.errorMessage(state.SessionID, "internal", "failed to save session"), saveErr
		}
		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Text{
				Text: &assistantpb.TextResponse{
					Text: "I am not fully sure what you need. I can help with weather, reminders, and table booking. Please rephrase your request.",
				},
			},
		}, nil
	}

	state.CurrentIntent = intentName
	assistantIntentDetectedTotal.WithLabelValues(state.CurrentIntent).Inc()
	assistantIntentConfidence.WithLabelValues(state.CurrentIntent).Observe(float64(confidence))

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
		serverMsg, err = s.handleWeatherIntent(ctx, state)
	case "create_reminder":
		serverMsg, err = s.handleReminderIntent(ctx, state, msg)
	case "book_table":
		serverMsg, err = s.handleBookingIntent(ctx, state)
	default:
		serverMsg = &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Text{
				Text: &assistantpb.TextResponse{
					Text: "I can do weather, reminders, and table booking.",
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

func (s *ConversationServer) handleWeatherIntent(ctx context.Context, state *SessionState) (*assistantpb.ServerMessage, error) {
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

func (s *ConversationServer) handleBookingIntent(ctx context.Context, state *SessionState) (*assistantpb.ServerMessage, error) {
	place := ""
	if v, ok := state.Entities["place"]; ok {
		place = v.GetStringValue()
	}
	persons := int32(2)
	if v, ok := state.Entities["persons"]; ok {
		persons = int32(v.GetIntValue())
		if persons < 1 {
			persons = 2
		}
	}
	var missing []string
	if place == "" {
		missing = append(missing, "place")
	}
	if len(missing) > 0 {
		state.MissingEntities = missing
		state.Status = StatusCollecting
		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Confirmation{
				Confirmation: &assistantpb.ConfirmationRequest{
					Question: "Where would you like to book? (e.g. La Scala)",
				},
			},
		}, nil
	}
	state.Status = StatusConfirming
	state.MissingEntities = nil
	q := "Book a table at " + place + " for " + formatPersons(persons) + ". Is that correct? (yes/no)"
	if err := s.stateStore.SaveSession(ctx, state); err != nil {
		return s.errorMessage(state.SessionID, "internal", "failed to save session"), err
	}
	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload: &assistantpb.ServerMessage_Confirmation{
			Confirmation: &assistantpb.ConfirmationRequest{Question: q},
		},
	}, nil
}

func formatPersons(n int32) string {
	if n == 1 {
		return "1 person"
	}
	return fmt.Sprintf("%d people", n)
}

// processCollecting handles dialog turns when we are missing some entities for the current intent.
// For Phase 1 it only collects the "location" entity for the weather intent.
func (s *ConversationServer) processCollecting(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	if switched, resp, err := s.tryIntentSwitch(ctx, state, msg); switched {
		return resp, err
	}

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

	switch state.CurrentIntent {
	case "get_weather":
		return s.processCollectingWeather(ctx, state)
	case "book_table":
		return s.processCollectingBooking(ctx, state)
	default:
		// Fallback: treat as weather (legacy)
		return s.processCollectingWeather(ctx, state)
	}
}

func (s *ConversationServer) processCollectingWeather(ctx context.Context, state *SessionState) (*assistantpb.ServerMessage, error) {
	location := ""
	if v, ok := state.Entities["location"]; ok {
		location = v.GetStringValue()
	}
	if location == "" {
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
	state.Status = StatusConfirming
	state.MissingEntities = nil
	question := "You want to know the weather in " + location + ". Is that correct? (yes/no)"
	if err := s.stateStore.SaveSession(ctx, state); err != nil {
		return s.errorMessage(state.SessionID, "internal", "failed to save session state"), err
	}
	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload:   &assistantpb.ServerMessage_Confirmation{Confirmation: &assistantpb.ConfirmationRequest{Question: question}},
	}, nil
}

func (s *ConversationServer) processCollectingBooking(ctx context.Context, state *SessionState) (*assistantpb.ServerMessage, error) {
	place := ""
	if v, ok := state.Entities["place"]; ok {
		place = v.GetStringValue()
	}
	persons := int32(2)
	if v, ok := state.Entities["persons"]; ok {
		persons = int32(v.GetIntValue())
		if persons < 1 {
			persons = 2
		}
	}
	if place == "" {
		state.Status = StatusCollecting
		state.MissingEntities = []string{"place"}
		return &assistantpb.ServerMessage{
			SessionId: state.SessionID,
			Payload: &assistantpb.ServerMessage_Confirmation{
				Confirmation: &assistantpb.ConfirmationRequest{
					Question: "Where would you like to book? (e.g. La Scala)",
				},
			},
		}, s.stateStore.SaveSession(ctx, state)
	}
	state.Status = StatusConfirming
	state.MissingEntities = nil
	q := "Book a table at " + place + " for " + formatPersons(persons) + ". Is that correct? (yes/no)"
	if err := s.stateStore.SaveSession(ctx, state); err != nil {
		return s.errorMessage(state.SessionID, "internal", "failed to save session"), err
	}
	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload:   &assistantpb.ServerMessage_Confirmation{Confirmation: &assistantpb.ConfirmationRequest{Question: q}},
	}, nil
}

// processConfirming handles yes/no answers when the system asks for confirmation.
func (s *ConversationServer) processConfirming(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (*assistantpb.ServerMessage, error) {
	answer := strings.TrimSpace(strings.ToLower(msg.GetText()))

	isYes := answer == "yes" || answer == "y" || answer == "yeah" || answer == "ok" || answer == "okay" || answer == "sure"
	isNo := answer == "no" || answer == "n" || answer == "nope"

	if !isYes && !isNo {
		if switched, resp, err := s.tryIntentSwitch(ctx, state, msg); switched {
			return resp, err
		}
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
		if state.CurrentIntent == "book_table" {
			delete(state.Entities, "place")
			delete(state.Entities, "persons")
			state.Status = StatusCollecting
			state.MissingEntities = []string{"place"}
			if err := s.stateStore.SaveSession(ctx, state); err != nil {
				return s.errorMessage(state.SessionID, "internal", "failed to save session state"), err
			}
			return &assistantpb.ServerMessage{
				SessionId: state.SessionID,
				Payload: &assistantpb.ServerMessage_Confirmation{
					Confirmation: &assistantpb.ConfirmationRequest{
						Question: "Okay, where would you like to book?",
					},
				},
			}, nil
		}
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
	switch state.CurrentIntent {
	case "get_weather":
		return s.handleWeatherIntent(ctx, state)
	case "book_table":
		return s.runBookingSaga(ctx, state)
	default:
		return s.handleWeatherIntent(ctx, state)
	}
}

func (s *ConversationServer) tryIntentSwitch(ctx context.Context, state *SessionState, msg *assistantpb.ClientMessage) (bool, *assistantpb.ServerMessage, error) {
	classResp, err := s.classifier.Classify(ctx, &assistantpb.ClassifyRequest{
		Utterance:       msg.GetText(),
		PreviousIntents: []string{state.CurrentIntent},
	})
	if err != nil {
		return false, nil, nil
	}

	newIntent := classResp.GetIntentName()
	confidence := classResp.GetConfidence()
	if newIntent == "" || newIntent == state.CurrentIntent || confidence < minIntentConfidence {
		return false, nil, nil
	}
	if _, ok := supportedIntents[newIntent]; !ok {
		return false, nil, nil
	}
	assistantIntentDetectedTotal.WithLabelValues(newIntent).Inc()

	// If user asks for a clearly different task mid-flow, switch the intent and start fresh for this turn.
	state.CurrentIntent = newIntent
	state.Status = StatusProcessing
	state.MissingEntities = nil
	state.Entities = make(map[string]*assistantpb.EntityValue)
	resp, procErr := s.processNewOrCompleted(ctx, state, msg)
	return true, resp, procErr
}

// runBookingSaga runs Reserve then Confirm; on Confirm failure it compensates by Cancel.
func (s *ConversationServer) runBookingSaga(ctx context.Context, state *SessionState) (*assistantpb.ServerMessage, error) {
	sagaStartedAt := time.Now()
	place := ""
	if v, ok := state.Entities["place"]; ok {
		place = v.GetStringValue()
	}
	persons := int32(2)
	if v, ok := state.Entities["persons"]; ok {
		persons = int32(v.GetIntValue())
		if persons < 1 {
			persons = 2
		}
	}
	if place == "" {
		return s.errorMessage(state.SessionID, "booking", "missing place"), nil
	}

	// Step 1: Reserve
	reserveResp, err := s.bookingClient.Reserve(ctx, &assistantpb.ReserveRequest{
		UserId:   state.UserID,
		Datetime: timestamppb.New(time.Now().UTC().Add(24 * time.Hour)),
		Persons:  persons,
		Place:    place,
	})
	if err != nil {
		assistantSagaFailures.WithLabelValues("booking", "reserve").Inc()
		assistantSagaDuration.WithLabelValues("booking", "failed").Observe(time.Since(sagaStartedAt).Seconds())
		return s.errorMessage(state.SessionID, "booking_unavailable", "reserve failed: "+err.Error()), err
	}
	bookingID := reserveResp.GetBooking().GetId()

	// Step 2: Confirm
	confirmResp, err := s.bookingClient.Confirm(ctx, &assistantpb.ConfirmRequest{
		UserId:    state.UserID,
		BookingId: bookingID,
	})
	if err != nil {
		// Compensate: cancel the reservation
		_, _ = s.bookingClient.Cancel(ctx, &assistantpb.CancelRequest{UserId: state.UserID, BookingId: bookingID})
		assistantSagaFailures.WithLabelValues("booking", "confirm").Inc()
		assistantSagaDuration.WithLabelValues("booking", "compensated").Observe(time.Since(sagaStartedAt).Seconds())
		return s.errorMessage(state.SessionID, "booking_confirm_failed", "confirmation failed, reservation cancelled"), err
	}
	if !confirmResp.GetSuccess() {
		_, _ = s.bookingClient.Cancel(ctx, &assistantpb.CancelRequest{UserId: state.UserID, BookingId: bookingID})
		assistantSagaFailures.WithLabelValues("booking", "confirm").Inc()
		assistantSagaDuration.WithLabelValues("booking", "compensated").Observe(time.Since(sagaStartedAt).Seconds())
		return s.errorMessage(state.SessionID, "booking_confirm_failed", "confirmation failed, reservation cancelled"), nil
	}

	state.Status = StatusCompleted
	_ = s.stateStore.SaveSession(ctx, state)
	assistantSagaDuration.WithLabelValues("booking", "completed").Observe(time.Since(sagaStartedAt).Seconds())
	text := "Done! Your table at " + place + " for " + formatPersons(persons) + " is confirmed (id: " + bookingID + ")."
	return &assistantpb.ServerMessage{
		SessionId: state.SessionID,
		Payload:   &assistantpb.ServerMessage_Text{Text: &assistantpb.TextResponse{Text: text}},
	}, nil
}


