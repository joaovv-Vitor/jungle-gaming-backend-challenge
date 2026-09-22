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
	registry      *prometheus.Registry
	sqsReceived   *prometheus.CounterVec
	sqsProcessed  *prometheus.CounterVec
	sqsProcessing *prometheus.HistogramVec
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
	registry.MustRegister(
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		received,
		processed,
		processing,
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
	return &Metrics{
		registry:      registry,
		sqsReceived:   received,
		sqsProcessed:  processed,
		sqsProcessing: processing,
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
