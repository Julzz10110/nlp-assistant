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

const (
	statusPending   = "pending"
	statusConfirmed = "confirmed"
	statusCancelled = "cancelled"
)

type bookingStore struct {
	mu        sync.RWMutex
	bookings  map[string]*assistantpb.Booking // id -> booking
	byUser    map[string][]string            // user_id -> booking_ids
}

func newBookingStore() *bookingStore {
	return &bookingStore{
		bookings: make(map[string]*assistantpb.Booking),
		byUser:   make(map[string][]string),
	}
}

func (s *bookingStore) reserve(userID string, datetime *timestamppb.Timestamp, persons int32, place string) *assistantpb.Booking {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := uuid.NewString()
	if datetime == nil {
		datetime = timestamppb.New(time.Now().UTC().Add(24 * time.Hour))
	}
	b := &assistantpb.Booking{
		Id:       id,
		UserId:   userID,
		Datetime: datetime,
		Persons:  persons,
		Place:    place,
		Status:   statusPending,
	}
	s.bookings[id] = b
	s.byUser[userID] = append(s.byUser[userID], id)
	return b
}

func (s *bookingStore) get(id string) *assistantpb.Booking {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bookings[id]
}

func (s *bookingStore) confirm(userID, bookingID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bookings[bookingID]
	if !ok || b.GetUserId() != userID || b.GetStatus() != statusPending {
		return false
	}
	b.Status = statusConfirmed
	return true
}

func (s *bookingStore) cancel(userID, bookingID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bookings[bookingID]
	if !ok || b.GetUserId() != userID {
		return false
	}
	b.Status = statusCancelled
	return true
}

type server struct {
	assistantpb.UnimplementedBookingServiceServer
	logger *logrus.Logger
	store  *bookingStore
}

func (s *server) Reserve(ctx context.Context, req *assistantpb.ReserveRequest) (*assistantpb.ReserveResponse, error) {
	b := s.store.reserve(req.GetUserId(), req.GetDatetime(), req.GetPersons(), req.GetPlace())
	s.logger.WithFields(logrus.Fields{
		"user_id": req.GetUserId(),
		"id":      b.GetId(),
		"place":   b.GetPlace(),
	}).Info("reserved booking")
	return &assistantpb.ReserveResponse{Booking: b}, nil
}

func (s *server) Confirm(ctx context.Context, req *assistantpb.ConfirmRequest) (*assistantpb.ConfirmResponse, error) {
	ok := s.store.confirm(req.GetUserId(), req.GetBookingId())
	var b *assistantpb.Booking
	if ok {
		b = s.store.get(req.GetBookingId())
	}
	s.logger.WithFields(logrus.Fields{
		"user_id": req.GetUserId(),
		"id":      req.GetBookingId(),
		"success": ok,
	}).Info("confirm booking")
	return &assistantpb.ConfirmResponse{Success: ok, Booking: b}, nil
}

func (s *server) Cancel(ctx context.Context, req *assistantpb.CancelRequest) (*assistantpb.CancelResponse, error) {
	ok := s.store.cancel(req.GetUserId(), req.GetBookingId())
	s.logger.WithFields(logrus.Fields{
		"user_id": req.GetUserId(),
		"id":      req.GetBookingId(),
		"success": ok,
	}).Info("cancel booking")
	return &assistantpb.CancelResponse{Success: ok}, nil
}

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	port := os.Getenv("BOOKING_PORT")
	if port == "" {
		port = "50056"
	}

	s := &server{logger: logger, store: newBookingStore()}
	grpcServer := grpc.NewServer()
	assistantpb.RegisterBookingServiceServer(grpcServer, s)

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	logger.WithField("port", port).Info("booking service listening")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("booking gRPC server stopped")
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down booking service...")

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		logger.Warn("force stop booking service")
		grpcServer.Stop()
	}
}
