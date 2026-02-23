package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
)

type server struct {
	assistantpb.UnimplementedIntentClassifierServer
	logger *logrus.Logger
}

func (s *server) Classify(ctx context.Context, req *assistantpb.ClassifyRequest) (*assistantpb.ClassifyResponse, error) {
	text := strings.ToLower(req.GetUtterance())

	intent := "unknown"
	var required []string

	if strings.Contains(text, "weather") || strings.Contains(text, "rain") {
		intent = "get_weather"
		required = []string{"location"}
	}

	s.logger.WithFields(logrus.Fields{
		"utterance": text,
		"intent":    intent,
	}).Info("classified utterance")

	return &assistantpb.ClassifyResponse{
		IntentName:       intent,
		Confidence:       0.9,
		RequiredEntities: required,
	}, nil
}

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	port := os.Getenv("CLASSIFIER_PORT")
	if port == "" {
		port = "50052"
	}

	grpcServer := grpc.NewServer()
	assistantpb.RegisterIntentClassifierServer(grpcServer, &server{logger: logger})

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	logger.WithField("port", port).Info("classifier listening")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("classifier gRPC server stopped")
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down classifier...")

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		logger.Warn("force stop classifier")
		grpcServer.Stop()
	}
}

