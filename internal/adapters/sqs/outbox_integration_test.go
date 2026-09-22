//go:build integration

package sqsadapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	postgresadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/postgres"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

func TestOutboxSenderPublishesEventToLocalStackFIFO(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := integrationSQSClient(t, ctx)
	queueName := "outbox-integration-" + integrationSuffix(t) + ".fifo"
	queueURL := createIntegrationQueue(t, client, queueName, fifoAttributes())
	cfg := integrationConsumerConfig("")
	cfg.SQSOutputQueue = queueName
	broker, err := NewBroker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sender := NewOutboxSender(broker, cfg)
	if err := sender.Check(ctx); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"eventId":"event-1","eventType":"WalletBalanceChanged"}`)
	if err := sender.Publish(ctx, outbox.Event{ID: "event-1", GroupID: "wallet-1", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	received := receiveIntegrationMessage(t, ctx, client, queueURL)
	if received.Body == nil || *received.Body != string(payload) {
		t.Fatalf("received payload = %v, want %s", received.Body, payload)
	}
}

func TestCommittedOutboxEventsArePublishedAndConfirmed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, _ := integrationFinancialServices(t, ctx)
	defer pool.Close()
	client := integrationSQSClient(t, ctx)
	queueName := "outbox-end-to-end-" + integrationSuffix(t) + ".fifo"
	queueURL := createIntegrationQueue(t, client, queueName, fifoAttributes())
	cfg := integrationConsumerConfig("")
	cfg.SQSOutputQueue = queueName
	cfg.OutboxLease = 30 * time.Second
	broker, err := NewBroker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sender := NewOutboxSender(broker, cfg)
	if err := sender.Check(ctx); err != nil {
		t.Fatal(err)
	}
	correlationID := "outbox-e2e-" + integrationID(t)
	_, err = wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: integrationID(t), Initial: integrationMoney(t, "100.00"), CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at='2000-01-01' WHERE correlation_id=$1`, correlationID); err != nil {
		t.Fatal(err)
	}
	service := outbox.NewService(postgresadapter.NewOutboxStore(postgresadapter.NewUnitOfWork(pool)), sender, config.Config{OutboxLease: 30 * time.Second})
	for range 2 {
		if outcome, err := service.ProcessOne(ctx); err != nil || outcome != outbox.OutcomePublished {
			t.Fatalf("publish = %s, error=%v", outcome, err)
		}
	}
	seen := make(map[string]bool)
	for range 2 {
		message := receiveIntegrationMessage(t, ctx, client, queueURL)
		var envelope struct {
			EventID string `json:"eventId"`
		}
		if err := json.Unmarshal([]byte(aws.ToString(message.Body)), &envelope); err != nil || envelope.EventID == "" || seen[envelope.EventID] {
			t.Fatalf("received event = %+v, error=%v", envelope, err)
		}
		seen[envelope.EventID] = true
		if _, err := client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle}); err != nil {
			t.Fatal(err)
		}
	}
	var confirmed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE correlation_id=$1 AND published_at IS NOT NULL`, correlationID).Scan(&confirmed); err != nil || confirmed != 2 {
		t.Fatalf("confirmed = %d, error=%v, want 2", confirmed, err)
	}
}
