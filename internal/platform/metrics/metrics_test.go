package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSQSConsumptionMetrics(t *testing.T) {
	instrumentation := New()
	instrumentation.RecordSQSReceived(1)
	instrumentation.RecordSQSReceived(2)
	instrumentation.ObserveSQSProcessing(SQSResultProcessed, 25*time.Millisecond)
	instrumentation.ObserveSQSProcessing(SQSResultDeleteError, 50*time.Millisecond)

	recorder := httptest.NewRecorder()
	instrumentation.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, sample := range []string{
		`wager_sqs_consumer_messages_received_total{delivery="first"} 1`,
		`wager_sqs_consumer_messages_received_total{delivery="redelivery"} 1`,
		`wager_sqs_consumer_messages_processed_total{result="processed"} 1`,
		`wager_sqs_consumer_messages_processed_total{result="delete_error"} 1`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("metrics body does not contain %q", sample)
		}
	}
}
