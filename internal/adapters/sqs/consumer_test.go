package sqsadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	applicationingestion "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/ingestion"
	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	domainwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
)

const consumerTestMessage = `{
  "messageId":"message-1",
  "type":"WagerTransactionRequested",
  "occurredAt":"2026-09-22T12:00:00Z",
  "data":{
    "providerId":"provider-a",
    "externalTransactionId":"external-1",
    "idempotencyKey":"provider-a:external-1",
    "playerId":"player-1",
    "walletId":"wallet-1",
    "roundId":"round-1",
    "gameId":"game-1",
    "kind":"BET",
    "money":{"amount":"10.00","currency":"BRL"}
  }
}`

func TestConsumerRetriesCommittedMessageAfterDeleteFailure(t *testing.T) {
	broker := &consumerTestBroker{deleteErrors: []error{errors.New("connection lost"), nil}}
	ingester := &consumerTestIngester{transaction: consumerTestTransaction(t)}
	instrumentation := metrics.New()
	consumer := newConsumer(broker, ingester, config.Config{
		SQSConsumerName: "wager-transactions", SQSProcessing: time.Second,
		SQSPingTimeout: time.Second, SQSConcurrency: 1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), instrumentation)
	consumer.queueURL = "queue-url"

	consumer.handle(context.Background(), Message{ID: "broker-1", Body: consumerTestMessage, ReceiptHandle: "receipt-1"})
	consumer.handle(context.Background(), Message{ID: "broker-1", Body: consumerTestMessage, ReceiptHandle: "receipt-2"})

	if ingester.calls != 2 {
		t.Fatalf("consume calls = %d, want 2", ingester.calls)
	}
	if broker.deleteCalls != 2 {
		t.Fatalf("delete calls = %d, want 2", broker.deleteCalls)
	}
	if broker.releaseCalls != 0 {
		t.Fatalf("release calls = %d, want 0", broker.releaseCalls)
	}
	metricsBody := gatherMetrics(t, instrumentation)
	for _, sample := range []string{
		`wager_sqs_consumer_messages_processed_total{result="delete_error"} 1`,
		`wager_sqs_consumer_messages_processed_total{result="replay"} 1`,
	} {
		if !strings.Contains(metricsBody, sample) {
			t.Errorf("metrics body does not contain %q", sample)
		}
	}
}

func TestConsumerLeavesInvalidMessageForRedrive(t *testing.T) {
	broker := &consumerTestBroker{}
	instrumentation := metrics.New()
	consumer := newConsumer(broker, &consumerTestIngester{}, config.Config{
		SQSConsumerName: "wager-transactions", SQSProcessing: time.Second,
		SQSPingTimeout: time.Second, SQSConcurrency: 1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), instrumentation)
	consumer.queueURL = "queue-url"

	consumer.handle(context.Background(), Message{ID: "broker-invalid", Body: `{}`, ReceiptHandle: "receipt-invalid"})

	if broker.deleteCalls != 0 || broker.releaseCalls != 0 {
		t.Fatalf("invalid message was acknowledged: deletes=%d releases=%d", broker.deleteCalls, broker.releaseCalls)
	}
	if !strings.Contains(gatherMetrics(t, instrumentation), `wager_sqs_consumer_messages_processed_total{result="invalid"} 1`) {
		t.Fatal("invalid result metric was not recorded")
	}
}

func TestConsumerBacksOffTransientFailuresButRedrivesPermanentConflicts(t *testing.T) {
	broker := &consumerTestBroker{}
	ingester := &consumerTestIngester{err: errors.New("database temporarily unavailable")}
	consumer := newConsumer(broker, ingester, config.Config{
		SQSConsumerName: "wager-transactions", SQSProcessing: time.Second,
		SQSPingTimeout: time.Second, SQSConcurrency: 1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New())
	consumer.queueURL = "queue-url"

	for count := 1; count <= 3; count++ {
		consumer.handle(context.Background(), Message{
			ID: "broker-1", Body: consumerTestMessage, ReceiptHandle: "receipt-1", ReceiveCount: count,
		})
	}
	if len(broker.retryDelays) != 3 || broker.retryDelays[0] != 5 ||
		broker.retryDelays[1] != 10 || broker.retryDelays[2] != 20 {
		t.Fatalf("retry visibility delays = %v, want [5 10 20]", broker.retryDelays)
	}
	ingester.err = applicationwagering.ErrIdempotencyConflict
	consumer.handle(context.Background(), Message{
		ID: "broker-1", Body: consumerTestMessage, ReceiptHandle: "receipt-1", ReceiveCount: 4,
	})
	if len(broker.retryDelays) != 3 || broker.deleteCalls != 0 {
		t.Fatalf("permanent conflict was deferred or deleted: delays=%v deletes=%d", broker.retryDelays, broker.deleteCalls)
	}
}

func TestConsumerLogsExcludeUntrustedErrorDetails(t *testing.T) {
	const secret = "sensitive-consumer-error-sentinel"
	var logs bytes.Buffer
	broker := &consumerTestBroker{deleteErrors: []error{errors.New(secret)}}
	consumer := newConsumer(broker, &consumerTestIngester{err: errors.New(secret)}, config.Config{
		SQSConsumerName: "wager-transactions", SQSProcessing: time.Second,
		SQSPingTimeout: time.Second, SQSConcurrency: 1,
	}, slog.New(slog.NewJSONHandler(&logs, nil)), metrics.New())
	consumer.queueURL = "queue-url"

	consumer.handle(context.Background(), Message{ID: "invalid-broker-message", Body: `{"` + secret + `":true}`})
	consumer.handle(context.Background(), Message{ID: "processing-broker-message", Body: consumerTestMessage})
	consumer.ingestion = &consumerTestIngester{transaction: consumerTestTransaction(t)}
	consumer.handle(context.Background(), Message{ID: "delete-broker-message", Body: consumerTestMessage})

	for _, expected := range []string{
		"invalid SQS message left for redrive", "SQS message processing failed",
		"SQS message committed but delete failed",
	} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("missing worker log %q: %s", expected, logs.String())
		}
	}
	if strings.Contains(logs.String(), secret) || !strings.Contains(logs.String(), `"reason":"invalid_message"`) {
		t.Fatalf("untrusted error detail leaked or safe reason missing: %s", logs.String())
	}
}

func TestConsumerLimitsReceiveBatchToAvailableWorkers(t *testing.T) {
	consumer := newConsumer(&consumerTestBroker{}, &consumerTestIngester{}, config.Config{
		SQSReceiveBatch: 10, SQSConcurrency: 2,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New())
	consumer.semaphore <- struct{}{}

	batch, ok := consumer.availableBatch(context.Background())
	if !ok || batch != 1 {
		t.Fatalf("available batch = %d/%v, want 1/true", batch, ok)
	}

	consumer.semaphore <- struct{}{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if batch, ok := consumer.availableBatch(cancelled); ok || batch != 0 {
		t.Fatalf("cancelled available batch = %d/%v, want 0/false", batch, ok)
	}
}

func consumerTestTransaction(t *testing.T) *domainwagering.Transaction {
	t.Helper()
	amount, err := money.Parse("10.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := domainwagering.NewOpening("transaction-1", "wallet-1", "player-1", amount, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}

func gatherMetrics(t *testing.T, instrumentation *metrics.Metrics) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	instrumentation.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	if recorder.Code != 200 {
		t.Fatalf("metrics status = %d, want 200", recorder.Code)
	}
	return recorder.Body.String()
}

type consumerTestIngester struct {
	transaction *domainwagering.Transaction
	calls       int
	err         error
}

func (i *consumerTestIngester) Consume(context.Context, string, applicationingestion.Message) (applicationingestion.Result, error) {
	i.calls++
	if i.err != nil {
		return applicationingestion.Result{}, i.err
	}
	replay := i.calls > 1
	return applicationingestion.Result{
		WagerResult: applicationwagering.Result{Transaction: i.transaction, Replay: replay},
		Duplicate:   replay,
	}, nil
}

type consumerTestBroker struct {
	deleteErrors []error
	deleteCalls  int
	releaseCalls int
	retryDelays  []int32
}

func (*consumerTestBroker) QueueURL(context.Context, string) (string, error)           { return "queue-url", nil }
func (*consumerTestBroker) Ping(context.Context, string) error                         { return nil }
func (*consumerTestBroker) ApproximateMessages(context.Context, string) (int64, error) { return 0, nil }
func (*consumerTestBroker) Receive(context.Context, string, int32, int32, int32) ([]Message, error) {
	return nil, nil
}
func (b *consumerTestBroker) Delete(context.Context, string, string) error {
	index := b.deleteCalls
	b.deleteCalls++
	if index < len(b.deleteErrors) {
		return b.deleteErrors[index]
	}
	return nil
}
func (b *consumerTestBroker) Release(context.Context, string, string) error {
	b.releaseCalls++
	return nil
}
func (b *consumerTestBroker) ChangeVisibility(_ context.Context, _, _ string, seconds int32) error {
	b.retryDelays = append(b.retryDelays, seconds)
	return nil
}
