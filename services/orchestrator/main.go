package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
	"github.com/Julzz10110/nlp-assistant/internal/config"
	"github.com/Julzz10110/nlp-assistant/internal/telemetry"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	cfgPath := os.Getenv("ORCHESTRATOR_CONFIG")
	if cfgPath == "" {
		cfgPath = "config/orchestrator.yaml"
	}

	cfg, err := config.LoadOrchestratorConfig(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	shutdown, err := telemetry.InitTracerProvider(ctx, telemetry.Options{
		ServiceName: cfg.Service.Name,
		Endpoint:    cfg.OTEL.JaegerEndpoint,
		SampleRatio: cfg.OTEL.SampleRate,
	})
	if err != nil {
		logger.WithError(err).Warn("failed to init telemetry, continuing without OTEL")
	} else {
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdown(ctx); err != nil {
				logger.WithError(err).Error("failed to shutdown tracer provider")
			}
		}()
	}

	// gRPC clients (classifier, extractor, weather) будут использовать OTEL-интерсепторы.
	classifierConn, err := grpc.DialContext(
		ctx,
		cfg.Services.Classifier.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		logger.WithError(err).Fatal("failed to connect classifier service")
	}
	defer classifierConn.Close()

	extractorConn, err := grpc.DialContext(
		ctx,
		cfg.Services.Extractor.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		logger.WithError(err).Fatal("failed to connect extractor service")
	}
	defer extractorConn.Close()

	weatherConn, err := grpc.DialContext(
		ctx,
		cfg.Services.Weather.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		logger.WithError(err).Fatal("failed to connect weather service")
	}
	defer weatherConn.Close()

	stateStore := NewInMemoryStateStore()

	server := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)

	orchestratorSrv := NewConversationServer(
		logger,
		stateStore,
		assistantpb.NewIntentClassifierClient(classifierConn),
		assistantpb.NewEntityExtractorClient(extractorConn),
		assistantpb.NewWeatherServiceClient(weatherConn),
	)
	assistantpb.RegisterConversationOrchestratorServer(server, orchestratorSrv)

	addr := fmt.Sprintf(":%d", cfg.Service.Port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		logger.WithError(err).Fatal("failed to listen")
	}

	logger.WithField("addr", addr).Info("orchestrator listening")

	go func() {
		if err := server.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server stopped")
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down orchestrator...")

	stopped := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		logger.Warn("force stop gRPC server")
		server.Stop()
	}
}

