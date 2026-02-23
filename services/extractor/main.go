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
	assistantpb.UnimplementedEntityExtractorServer
	logger *logrus.Logger
}

func (s *server) Extract(ctx context.Context, req *assistantpb.ExtractRequest) (*assistantpb.ExtractResponse, error) {
	text := strings.ToLower(req.GetUtterance())
	entities := make(map[string]*assistantpb.EntityValue)

	// Very naive heuristic: if the text contains "tokyo".
	if strings.Contains(text, "tokyo") {
		entities["location"] = &assistantpb.EntityValue{
			Value: &assistantpb.EntityValue_StringValue{
				StringValue: "Tokyo",
			},
			Confidence:  0.9,
			SourceText:  "tokyo",
		}
	}

	s.logger.WithFields(logrus.Fields{
		"utterance": text,
		"entities":  len(entities),
	}).Info("extracted entities")

	return &assistantpb.ExtractResponse{
		Entities: entities,
	}, nil
}

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	port := os.Getenv("EXTRACTOR_PORT")
	if port == "" {
		port = "50053"
	}

	grpcServer := grpc.NewServer()
	assistantpb.RegisterEntityExtractorServer(grpcServer, &server{logger: logger})

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	logger.WithField("port", port).Info("extractor listening")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("extractor gRPC server stopped")
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down extractor...")

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		logger.Warn("force stop extractor")
		grpcServer.Stop()
	}
}

