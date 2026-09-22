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

func TestFinancialAndOperationalMetricsHaveBoundedLabels(t *testing.T) {
	instrumentation := New()
	instrumentation.RecordWager("http", "BET", "PROCESSED", "", false, time.Millisecond)
	instrumentation.RecordWager("http", "BET", "PROCESSED", "", true, time.Millisecond)
	instrumentation.RecordWager("http", "BET", "REJECTED", "BET_INSUFFICIENT_FUNDS", false, time.Millisecond)
	instrumentation.RecordWager("http", "unknown-wallet-id", "PROCESSED", "", false, time.Millisecond)
	instrumentation.RecordWagerError("http", "idempotency_conflict")
	instrumentation.RecordReferenceAttempt("rescheduled")
	instrumentation.RecordOutboxAttempt("republished")
	instrumentation.SetOutboxBacklog(3, 12.5)
	instrumentation.SetDLQDepth(2)
	instrumentation.RecordDependencyFailure("postgres")
	instrumentation.RecordReconciliation("mismatch")

	recorder := httptest.NewRecorder()
	instrumentation.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, sample := range []string{
		`wager_transactions_committed_total{kind="BET",source="http",status="PROCESSED"} 1`,
		`wager_transactions_replays_total{source="http"} 1`,
		`wager_transactions_failures_total{code="BET_INSUFFICIENT_FUNDS",source="http"} 1`,
		`wager_outbox_pending_events 3`,
		`wager_outbox_oldest_pending_age_seconds 12.5`,
		`wager_sqs_consumer_dlq_messages_approximate 2`,
		`wager_outbox_attempts_total{result="republished"} 1`,
		`wager_reconciliation_checks_total{result="mismatch"} 1`,
	} {
		if !strings.Contains(body, sample) {
			t.Errorf("metrics body does not contain %q", sample)
		}
	}
	if strings.Contains(body, "unknown-wallet-id") {
		t.Fatal("dynamic identifier appeared in a metric label")
	}
}
