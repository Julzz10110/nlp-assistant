package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
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
	initMetrics()

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

	reminderConn, err := grpc.DialContext(
		ctx,
		cfg.Services.Reminder.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		logger.WithError(err).Fatal("failed to connect reminder service")
	}
	defer reminderConn.Close()

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

	bookingConn, err := grpc.DialContext(
		ctx,
		cfg.Services.Booking.Address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		logger.WithError(err).Fatal("failed to connect booking service")
	}
	defer bookingConn.Close()

	var stateStore StateStore = NewInMemoryStateStore()
	if cfg.Redis.Address != "" {
		redisStore := NewRedisStateStore(cfg)
		if err := redisStore.Ping(ctx); err != nil {
			logger.WithError(err).Warn("failed to connect Redis, falling back to in-memory state store")
		} else {
			logger.WithField("addr", cfg.Redis.Address).Info("using Redis state store")
			stateStore = redisStore
		}
	}

	server := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)

	orchestratorSrv := NewConversationServer(
		logger,
		stateStore,
		assistantpb.NewIntentClassifierClient(classifierConn),
		assistantpb.NewEntityExtractorClient(extractorConn),
		assistantpb.NewWeatherServiceClient(weatherConn),
		assistantpb.NewReminderServiceClient(reminderConn),
		assistantpb.NewBookingServiceClient(bookingConn),
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

	metricsPort := os.Getenv("ORCHESTRATOR_METRICS_PORT")
	if metricsPort == "" {
		metricsPort = "9091"
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:    ":" + metricsPort,
		Handler: metricsMux,
	}
	go func() {
		logger.WithField("addr", ":"+metricsPort).Info("orchestrator metrics listening")
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Error("metrics server stopped")
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
	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelShutdown()
	_ = metricsServer.Shutdown(ctxShutdown)
}

