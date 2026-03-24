package main

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	metricsOnce sync.Once

	assistantRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "assistant_requests_total",
			Help: "Total assistant requests grouped by intent and status.",
		},
		[]string{"intent", "status"},
	)

	assistantRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "assistant_request_duration_seconds",
			Help:    "Assistant request latency in seconds grouped by intent.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"intent"},
	)

	assistantIntentConfidence = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "assistant_intent_confidence",
			Help:    "Intent classifier confidence by detected intent.",
			Buckets: []float64{0.1, 0.3, 0.5, 0.7, 0.85, 0.95, 1.0},
		},
		[]string{"intent"},
	)

	assistantIntentDetectedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "assistant_intent_detected_total",
			Help: "Total number of detected intents grouped by intent.",
		},
		[]string{"intent"},
	)

	assistantFallbackTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "assistant_fallback_total",
			Help: "Total number of fallback responses grouped by reason.",
		},
		[]string{"reason"},
	)

	assistantActiveSessions = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "assistant_sessions_active",
			Help: "Number of active sessions observed by orchestrator.",
		},
	)

	assistantSagaDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "assistant_saga_duration_seconds",
			Help:    "Saga execution duration in seconds by saga name and status.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"saga", "status"},
	)

	assistantSagaFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "assistant_saga_failures_total",
			Help: "Total number of saga failures by saga name and stage.",
		},
		[]string{"saga", "stage"},
	)
)

func initMetrics() {
	metricsOnce.Do(func() {
		prometheus.MustRegister(
			assistantRequestsTotal,
			assistantRequestDuration,
			assistantIntentConfidence,
			assistantIntentDetectedTotal,
			assistantFallbackTotal,
			assistantActiveSessions,
			assistantSagaDuration,
			assistantSagaFailures,
		)
	})
}

func observeRequest(intent, status string, startedAt time.Time) {
	if intent == "" {
		intent = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	assistantRequestsTotal.WithLabelValues(intent, status).Inc()
	assistantRequestDuration.WithLabelValues(intent).Observe(time.Since(startedAt).Seconds())
}

