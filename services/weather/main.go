package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"

	assistantpb "github.com/Julzz10110/nlp-assistant/api/proto/assistantpb"
)

type server struct {
	assistantpb.UnimplementedWeatherServiceServer
	logger *logrus.Logger
}

func (s *server) GetWeather(ctx context.Context, req *assistantpb.WeatherRequest) (*assistantpb.WeatherResponse, error) {
	location := req.GetLocation()
	if location == "" {
		location = "unknown place"
	}

	// For MVP we generate pseudo-random weather based on the location.
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	temp := 10 + r.Float64()*15
	summary := fmt.Sprintf("In %s today it's %.1f°C with scattered clouds", location, temp)

	s.logger.WithFields(logrus.Fields{
		"location":  location,
		"summary":   summary,
		"temperature": temp,
	}).Info("returning weather")

	return &assistantpb.WeatherResponse{
		Location:     location,
		Date:         req.GetDate(),
		Summary:      summary,
		TemperatureC: temp,
	}, nil
}

func main() {
	logger := logrus.New()
	logger.SetFormatter(&logrus.JSONFormatter{})

	port := os.Getenv("WEATHER_PORT")
	if port == "" {
		port = "50054"
	}

	grpcServer := grpc.NewServer()
	assistantpb.RegisterWeatherServiceServer(grpcServer, &server{logger: logger})

	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	logger.WithField("port", port).Info("weather service listening")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("weather gRPC server stopped")
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down weather service...")

	stopped := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		logger.Warn("force stop weather service")
		grpcServer.Stop()
	}
}

