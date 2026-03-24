package main

import (
	"context"
	"testing"
	"time"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type classifierStub struct {
	intentByUtterance map[string]string
	confByUtterance   map[string]float32
}

func (s *classifierStub) Classify(_ context.Context, in *assistantpb.ClassifyRequest, _ ...grpc.CallOption) (*assistantpb.ClassifyResponse, error) {
	intent := s.intentByUtterance[in.GetUtterance()]
	conf := s.confByUtterance[in.GetUtterance()]
	return &assistantpb.ClassifyResponse{
		IntentName: intent,
		Confidence: conf,
	}, nil
}

type extractorStub struct {
	entitiesByUtterance map[string]map[string]*assistantpb.EntityValue
}

func (s *extractorStub) Extract(_ context.Context, in *assistantpb.ExtractRequest, _ ...grpc.CallOption) (*assistantpb.ExtractResponse, error) {
	entities := s.entitiesByUtterance[in.GetUtterance()]
	if entities == nil {
		entities = map[string]*assistantpb.EntityValue{}
	}
	return &assistantpb.ExtractResponse{Entities: entities}, nil
}

type weatherStub struct {
	summary string
}

func (s *weatherStub) GetWeather(_ context.Context, _ *assistantpb.WeatherRequest, _ ...grpc.CallOption) (*assistantpb.WeatherResponse, error) {
	return &assistantpb.WeatherResponse{
		Summary: s.summary,
		Date:    timestamppb.New(time.Now().UTC()),
	}, nil
}

type reminderStub struct{}

func (s *reminderStub) CreateReminder(_ context.Context, in *assistantpb.CreateReminderRequest, _ ...grpc.CallOption) (*assistantpb.CreateReminderResponse, error) {
	return &assistantpb.CreateReminderResponse{
		Reminder: &assistantpb.Reminder{
			Id:       "r-1",
			UserId:   in.GetUserId(),
			Text:     in.GetText(),
			Datetime: timestamppb.New(time.Now().UTC()),
		},
	}, nil
}
func (s *reminderStub) ListReminders(context.Context, *assistantpb.ListRemindersRequest, ...grpc.CallOption) (*assistantpb.ListRemindersResponse, error) {
	return &assistantpb.ListRemindersResponse{}, nil
}
func (s *reminderStub) CancelReminder(context.Context, *assistantpb.CancelReminderRequest, ...grpc.CallOption) (*assistantpb.CancelReminderResponse, error) {
	return &assistantpb.CancelReminderResponse{Success: true}, nil
}

type bookingStub struct{}

func (s *bookingStub) Reserve(context.Context, *assistantpb.ReserveRequest, ...grpc.CallOption) (*assistantpb.ReserveResponse, error) {
	return &assistantpb.ReserveResponse{
		Booking: &assistantpb.Booking{Id: "b-1"},
	}, nil
}
func (s *bookingStub) Confirm(context.Context, *assistantpb.ConfirmRequest, ...grpc.CallOption) (*assistantpb.ConfirmResponse, error) {
	return &assistantpb.ConfirmResponse{Success: true}, nil
}
func (s *bookingStub) Cancel(context.Context, *assistantpb.CancelRequest, ...grpc.CallOption) (*assistantpb.CancelResponse, error) {
	return &assistantpb.CancelResponse{Success: true}, nil
}

func newTestServer() *ConversationServer {
	return NewConversationServer(
		logrus.New(),
		NewInMemoryStateStore(),
		&classifierStub{
			intentByUtterance: map[string]string{},
			confByUtterance:   map[string]float32{},
		},
		&extractorStub{entitiesByUtterance: map[string]map[string]*assistantpb.EntityValue{}},
		&weatherStub{summary: "Sunny and warm"},
		&reminderStub{},
		&bookingStub{},
	)
}

func TestHandleClientMessage_LowConfidenceFallback(t *testing.T) {
	srv := newTestServer()
	srv.classifier = &classifierStub{
		intentByUtterance: map[string]string{"help me": "get_weather"},
		confByUtterance:   map[string]float32{"help me": 0.2},
	}

	resp, err := srv.handleClientMessage(context.Background(), &assistantpb.ClientMessage{
		SessionId: "s-1",
		UserId:    "u-1",
		Text:      "help me",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetText() == nil {
		t.Fatalf("expected text response, got: %#v", resp.GetPayload())
	}
	if got := resp.GetText().GetText(); got == "" {
		t.Fatalf("expected fallback text, got empty")
	}
}

func TestProcessCollecting_CanSwitchIntent(t *testing.T) {
	srv := newTestServer()
	srv.classifier = &classifierStub{
		intentByUtterance: map[string]string{
			"what is weather in Paris": "get_weather",
		},
		confByUtterance: map[string]float32{
			"what is weather in Paris": 0.93,
		},
	}
	srv.extractor = &extractorStub{
		entitiesByUtterance: map[string]map[string]*assistantpb.EntityValue{
			"what is weather in Paris": {
				"location": {Value: &assistantpb.EntityValue_StringValue{StringValue: "Paris"}},
			},
		},
	}

	state := &SessionState{
		SessionID:     "s-2",
		UserID:        "u-2",
		Status:        StatusCollecting,
		CurrentIntent: "book_table",
		Entities:      map[string]*assistantpb.EntityValue{},
	}
	if err := srv.stateStore.SaveSession(context.Background(), state); err != nil {
		t.Fatalf("save state: %v", err)
	}

	resp, err := srv.handleClientMessage(context.Background(), &assistantpb.ClientMessage{
		SessionId: "s-2",
		UserId:    "u-2",
		Text:      "what is weather in Paris",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetText() == nil {
		t.Fatalf("expected text response, got: %#v", resp.GetPayload())
	}
	if got := resp.GetText().GetText(); got != "Sunny and warm" {
		t.Fatalf("expected weather summary, got %q", got)
	}
}
