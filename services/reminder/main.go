package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
)

type reminderStore struct {
	mu        sync.RWMutex
	reminders map[string][]*assistantpb.Reminder // user_id -> reminders
}

func newReminderStore() *reminderStore {
	return &reminderStore{
		reminders: make(map[string][]*assistantpb.Reminder),
	}
}

func (s *reminderStore) add(userID string, r *assistantpb.Reminder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reminders[userID] = append(s.reminders[userID], r)
}

func (s *reminderStore) list(userID string) []*assistantpb.Reminder {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]*assistantpb.Reminder(nil), s.reminders[userID]...)
}

func (s *reminderStore) cancel(userID, reminderID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.reminders[userID]
	if len(list) == 0 {
		return false
	}

	out := list[:0]
	var removed bool
	for _, r := range list {
		if r.GetId() == reminderID {
			removed = true
			continue
		}
		out = append(out, r)
	}
	if removed {
		s.reminders[userID] = out
	}
	return removed
}

type server struct {
	assistantpb.UnimplementedReminderServiceServer

	logger *logrus.Logger
	store  *reminderStore
}

func (s *server) CreateReminder(ctx context.Context, req *assistantpb.CreateReminderRequest) (*assistantpb.CreateReminderResponse, error) {
	id := uuid.NewString()
	now := time.Now().UTC()
	dt := req.GetDatetime()
	if dt == nil {
		dt = timestamppb.New(now.Add(1 * time.Hour))
	}

	rem := &assistantpb.Reminder{
		Id:            id,
		UserId:        req.GetUserId(),
		Text:          req.GetText(),
		Datetime:      dt,
		RepeatPattern: req.GetRepeatPattern(),
	}

	s.store.add(req.GetUserId(), rem)

	s.logger.WithFields(logrus.Fields{
		"user_id": req.GetUserId(),
		"id":      rem.GetId(),
	}).Info("created reminder")

	return &assistantpb.CreateReminderResponse{
		Reminder: rem,
	}, nil
}

func (s *server) ListReminders(ctx context.Context, req *assistantpb.ListRemindersRequest) (*assistantpb.ListRemindersResponse, error) {
	list := s.store.list(req.GetUserId())
	return &assistantpb.ListRemindersResponse{
		Reminders: list,
	}, nil
}

func (s *server) CancelReminder(ctx context.Context, req *assistantpb.CancelReminderRequest) (*assistantpb.CancelReminderResponse, error) {
	ok := s.store.cancel(req.GetUserId(), req.GetReminderId())
	s.logger.WithFields(logrus.Fields{
		"user_id": req.GetUserId(),
		"id":      req.GetReminderId(),
		"success": ok,
	}).Info("cancel reminder")

	return &assistantpb.CancelReminderResponse{
		Success: ok,
	}, nil
}

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	port := os.Getenv("REMINDER_PORT")
	if port == "" {
		port = "50055"
	}

	s := &server{
		logger: logger,
		store:  newReminderStore(),
	}

	grpcServer := grpc.NewServer()
	assistantpb.RegisterReminderServiceServer(grpcServer, s)

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	logger.WithField("port", port).Info("reminder service listening")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("reminder gRPC server stopped")
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down reminder service...")

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		logger.Warn("force stop reminder service")
		grpcServer.Stop()
	}
}

