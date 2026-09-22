package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type SQSResult string

const (
	SQSResultProcessed       SQSResult = "processed"
	SQSResultReplay          SQSResult = "replay"
	SQSResultInvalid         SQSResult = "invalid"
	SQSResultProcessingError SQSResult = "processing_error"
	SQSResultDeleteError     SQSResult = "delete_error"
)

type Metrics struct {
	registry       *prometheus.Registry
	sqsReceived    *prometheus.CounterVec
	sqsProcessed   *prometheus.CounterVec
	sqsProcessing  *prometheus.HistogramVec
	reconciliation *prometheus.CounterVec
	wagers         *prometheus.CounterVec
	wagerDuration  *prometheus.HistogramVec
	wagerReplays   *prometheus.CounterVec
	wagerFailures  *prometheus.CounterVec
	references     *prometheus.CounterVec
	outboxAttempts *prometheus.CounterVec
	outboxPending  prometheus.Gauge
	outboxAge      prometheus.Gauge
	dependencies   *prometheus.CounterVec
	shutdown       *prometheus.HistogramVec
	dlqDepth       prometheus.Gauge
}

func New() *Metrics {
	registry := prometheus.NewRegistry()
	received := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager",
		Subsystem: "sqs_consumer",
		Name:      "messages_received_total",
		Help:      "Number of SQS messages received by delivery type.",
	}, []string{"delivery"})
	processed := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager",
		Subsystem: "sqs_consumer",
		Name:      "messages_processed_total",
		Help:      "Number of SQS message handling attempts by result.",
	}, []string{"result"})
	processing := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "wager",
		Subsystem: "sqs_consumer",
		Name:      "message_processing_duration_seconds",
		Help:      "Duration of SQS message handling attempts by result.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"result"})
	reconciliation := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "reconciliation", Name: "checks_total",
		Help: "Number of wallet reconciliations by result.",
	}, []string{"result"})
	wagers := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "transactions", Name: "committed_total",
		Help: "New committed wagering results by transport, kind and status.",
	}, []string{"source", "kind", "status"})
	wagerDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "wager", Subsystem: "transactions", Name: "processing_duration_seconds",
		Help: "Time to commit a wagering operation by transport and result.", Buckets: prometheus.DefBuckets,
	}, []string{"source", "status"})
	wagerReplays := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "transactions", Name: "replays_total",
		Help: "Idempotent financial replays by transport.",
	}, []string{"source"})
	wagerFailures := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "transactions", Name: "failures_total",
		Help: "Business rejections and transaction errors by transport and bounded code.",
	}, []string{"source", "code"})
	references := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "reference", Name: "attempts_total",
		Help: "Reference worker attempts by result.",
	}, []string{"result"})
	outboxAttempts := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "outbox", Name: "attempts_total",
		Help: "Outbox publication attempts by result.",
	}, []string{"result"})
	outboxPending := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "wager", Subsystem: "outbox", Name: "pending_events",
		Help: "Number of unpublished outbox events.",
	})
	outboxAge := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "wager", Subsystem: "outbox", Name: "oldest_pending_age_seconds",
		Help: "Age of the oldest unpublished outbox event.",
	})
	dependencies := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "wager", Subsystem: "health", Name: "dependency_failures_total",
		Help: "Failed readiness checks by dependency.",
	}, []string{"dependency"})
	shutdown := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "wager", Subsystem: "lifecycle", Name: "shutdown_duration_seconds",
		Help: "Graceful shutdown duration by component.", Buckets: prometheus.DefBuckets,
	}, []string{"component"})
	dlqDepth := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "wager", Subsystem: "sqs_consumer", Name: "dlq_messages_approximate",
		Help: "Approximate number of visible messages in the SQS dead-letter queue.",
	})
	registry.MustRegister(
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		received,
		processed,
		processing,
		reconciliation,
		wagers, wagerDuration, wagerReplays, wagerFailures, references,
		outboxAttempts, outboxPending, outboxAge, dependencies, shutdown, dlqDepth,
	)
	for _, delivery := range []string{"first", "redelivery"} {
		received.WithLabelValues(delivery)
	}
	for _, result := range []SQSResult{
		SQSResultProcessed,
		SQSResultReplay,
		SQSResultInvalid,
		SQSResultProcessingError,
		SQSResultDeleteError,
	} {
		processed.WithLabelValues(string(result))
		processing.WithLabelValues(string(result))
	}
	for _, result := range []string{"consistent", "mismatch", "overflow", "error"} {
		reconciliation.WithLabelValues(result)
	}
	for _, source := range []string{"http", "sqs", "internal"} {
		wagerReplays.WithLabelValues(source)
		for _, status := range []string{"PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED"} {
			wagerDuration.WithLabelValues(source, status)
		}
	}
	for _, result := range []string{"completed", "rescheduled", "stale", "error"} {
		references.WithLabelValues(result)
	}
	for _, result := range []string{"published", "republished", "retry", "error"} {
		outboxAttempts.WithLabelValues(result)
	}
	for _, dependency := range []string{"postgres", "oidc", "sqs", "outbox_queue"} {
		dependencies.WithLabelValues(dependency)
	}
	return &Metrics{
		registry:       registry,
		sqsReceived:    received,
		sqsProcessed:   processed,
		sqsProcessing:  processing,
		reconciliation: reconciliation,
		wagers:         wagers, wagerDuration: wagerDuration, wagerReplays: wagerReplays,
		wagerFailures: wagerFailures, references: references,
		outboxAttempts: outboxAttempts, outboxPending: outboxPending, outboxAge: outboxAge,
		dependencies: dependencies, shutdown: shutdown,
		dlqDepth: dlqDepth,
	}
}

func (m *Metrics) SetDLQDepth(count int64) {
	m.dlqDepth.Set(float64(count))
}

func (m *Metrics) RecordWager(source, kind, status, failure string, replay bool, elapsed time.Duration) {
	source = bounded(source, "http", "sqs", "internal")
	kind = bounded(kind, "OPENING", "BET", "WIN", "LOSS", "REFUND", "ROLLBACK")
	status = bounded(status, "PROCESSED", "REJECTED", "PENDING_REFERENCE", "FAILED")
	m.wagerDuration.WithLabelValues(source, status).Observe(elapsed.Seconds())
	if replay {
		m.wagerReplays.WithLabelValues(source).Inc()
		return
	}
	m.wagers.WithLabelValues(source, kind, status).Inc()
	if status == "REJECTED" && failure != "" {
		m.wagerFailures.WithLabelValues(source, bounded(failure,
			"BET_INSUFFICIENT_FUNDS", "REVERSAL_INSUFFICIENT_FUNDS", "MONEY_OVERFLOW",
			"REFERENCE_NOT_FOUND", "REFERENCE_NOT_PROCESSED", "REFERENCE_MISMATCH",
			"REFERENCE_TYPE_NOT_ALLOWED", "INVALID_OPERATION_AMOUNT", "ALREADY_REVERSED",
			"CURRENCY_MISMATCH", "WALLET_PLAYER_MISMATCH", "INVALID_REFERENCE",
			"INFRASTRUCTURE_PERMANENT_FAILURE")).Inc()
	}
}

func (m *Metrics) RecordWagerError(source, code string) {
	m.wagerFailures.WithLabelValues(bounded(source, "http", "sqs"), bounded(code,
		"idempotency_conflict", "external_id_conflict", "message_conflict",
		"concurrent_write", "invalid", "error")).Inc()
}

func (m *Metrics) RecordReferenceAttempt(result string) {
	m.references.WithLabelValues(bounded(result, "completed", "rescheduled", "stale", "error")).Inc()
}

func (m *Metrics) RecordOutboxAttempt(result string) {
	m.outboxAttempts.WithLabelValues(bounded(result, "published", "republished", "retry", "error")).Inc()
}

func (m *Metrics) SetOutboxBacklog(pending int64, oldestAgeSeconds float64) {
	m.outboxPending.Set(float64(pending))
	m.outboxAge.Set(oldestAgeSeconds)
}

func (m *Metrics) RecordDependencyFailure(dependency string) {
	m.dependencies.WithLabelValues(bounded(dependency, "postgres", "oidc", "sqs", "outbox_queue")).Inc()
}

func (m *Metrics) ObserveShutdown(component string, elapsed time.Duration) {
	m.shutdown.WithLabelValues(bounded(component, "http", "sqs", "outbox", "reference")).Observe(elapsed.Seconds())
}

func bounded(value string, accepted ...string) string {
	for _, candidate := range accepted {
		if value == candidate {
			return value
		}
	}
	return "other"
}

func (m *Metrics) RecordReconciliation(result string) {
	switch result {
	case "consistent", "mismatch", "overflow", "error":
		m.reconciliation.WithLabelValues(result).Inc()
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{EnableOpenMetrics: true})
}

func (m *Metrics) RecordSQSReceived(receiveCount int) {
	delivery := "first"
	if receiveCount > 1 {
		delivery = "redelivery"
	}
	m.sqsReceived.WithLabelValues(delivery).Inc()
}

func (m *Metrics) ObserveSQSProcessing(result SQSResult, elapsed time.Duration) {
	label := string(result)
	m.sqsProcessed.WithLabelValues(label).Inc()
	m.sqsProcessing.WithLabelValues(label).Observe(elapsed.Seconds())
}
