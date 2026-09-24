//go:build integration

package sqsadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	postgresadapter "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/postgres"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/outbox"
	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
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

func TestFourEventContractsReachSQSFromFinancialOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, wallets, _ := integrationFinancialServices(t, ctx)
	defer pool.Close()
	unit := postgresadapter.NewUnitOfWork(pool)
	wagers := applicationwagering.NewService(postgresadapter.NewWagerStore(unit,
		postgresadapter.NewWalletRepository(), postgresadapter.NewWagerRepository(),
		postgresadapter.NewLedgerRepository(), postgresadapter.NewOutboxRepository()))

	client := integrationSQSClient(t, ctx)
	queueName := "event-contracts-" + integrationSuffix(t) + ".fifo"
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

	correlationID := "event-contracts-" + integrationID(t)
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: integrationID(t), Initial: integrationMoney(t, "100.00"), CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	submit := func(externalID string, kind domain.Kind, amount, reference string) *domain.Transaction {
		t.Helper()
		result, err := wagers.Submit(ctx, applicationwagering.SubmitInput{
			ProviderID: "provider-a", ExternalTransactionID: externalID,
			IdempotencyKey: externalID, WalletID: account.ID(), PlayerID: account.PlayerID(),
			RoundID: "event-contracts-round", GameID: "event-contracts-game",
			Kind: kind, Amount: integrationMoney(t, amount),
			ReferenceExternalTransactionID: reference, CorrelationID: correlationID,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result.Transaction
	}
	bet := submit("bet-"+integrationID(t), domain.KindBet, "25.00", "")
	loss := submit("loss-"+integrationID(t), domain.KindLoss, "0.00", "")
	rejected := submit("rejected-"+integrationID(t), domain.KindBet, "100.00", "")
	missingReference := "missing-" + integrationID(t)
	pending := submit("pending-"+integrationID(t), domain.KindRefund, "25.00", missingReference)
	if bet.Status() != domain.StatusProcessed || loss.Status() != domain.StatusProcessed ||
		rejected.Status() != domain.StatusRejected || pending.Status() != domain.StatusPendingReference {
		t.Fatalf("unexpected transaction states: bet=%s loss=%s rejected=%s pending=%s",
			bet.Status(), loss.Status(), rejected.Status(), pending.Status())
	}

	rows, err := pool.Query(ctx, `SELECT event_id::text, payload FROM outbox_events WHERE correlation_id=$1`, correlationID)
	if err != nil {
		t.Fatal(err)
	}
	expected := make(map[string][]byte)
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		expected[id] = payload
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(expected) != 7 {
		t.Fatalf("outbox event count = %d, want 7", len(expected))
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at='2000-01-01' WHERE correlation_id=$1`, correlationID); err != nil {
		t.Fatal(err)
	}
	service := outbox.NewService(postgresadapter.NewOutboxStore(unit), sender, config.Config{OutboxLease: 30 * time.Second})
	for range len(expected) {
		if outcome, err := service.ProcessOne(ctx); err != nil || outcome != outbox.OutcomePublished {
			t.Fatalf("publish = %s, error=%v", outcome, err)
		}
	}

	seen := make(map[string]bool)
	counts := make(map[string]int)
	processedEventByTransaction := make(map[string]string)
	balanceCausationByTransaction := make(map[string]string)
	openingTransactionID := ""
	for range len(expected) {
		message := receiveIntegrationMessage(t, ctx, client, queueURL)
		body := []byte(aws.ToString(message.Body))
		var event struct {
			EventID       string `json:"eventId"`
			EventType     string `json:"eventType"`
			AggregateID   string `json:"aggregateId"`
			CorrelationID string `json:"correlationId"`
			CausationID   string `json:"causationId"`
			OccurredAt    string `json:"occurredAt"`
			Version       int    `json:"version"`
			Data          struct {
				TransactionID                  string `json:"transactionId"`
				WalletID                       string `json:"walletId"`
				Kind                           string `json:"kind"`
				Status                         string `json:"status"`
				FailureCode                    string `json:"failureCode"`
				ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
				WalletVersion                  int64  `json:"walletVersion"`
				BalanceBefore                  struct {
					Amount string `json:"amount"`
				} `json:"balanceBefore"`
				BalanceAfter struct {
					Amount string `json:"amount"`
				} `json:"balanceAfter"`
				Balance struct {
					Amount string `json:"amount"`
				} `json:"balance"`
				Money struct {
					Amount   string `json:"amount"`
					Currency string `json:"currency"`
				} `json:"money"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		stored, ok := expected[event.EventID]
		if !ok || seen[event.EventID] || !bytes.Equal(body, stored) {
			t.Fatalf("unexpected, duplicate or changed SQS event %s: %s", event.EventID, body)
		}
		seen[event.EventID] = true
		if event.CorrelationID != correlationID || event.Version != 1 {
			t.Fatalf("event metadata = %+v", event)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.OccurredAt); err != nil {
			t.Fatalf("event occurredAt = %q: %v", event.OccurredAt, err)
		}
		counts[event.EventType]++
		switch event.EventType {
		case "WagerTransactionProcessed":
			if event.AggregateID != event.Data.TransactionID || event.Data.Status != "PROCESSED" || event.Data.Money.Currency != "BRL" {
				t.Fatalf("processed contract = %+v", event)
			}
			processedEventByTransaction[event.Data.TransactionID] = event.EventID
			switch event.Data.TransactionID {
			case bet.ID():
				if event.Data.Kind != "BET" || event.Data.Money.Amount != "25.00" || event.Data.Balance.Amount != "75.00" {
					t.Fatalf("BET contract = %+v", event)
				}
			case loss.ID():
				if event.Data.Kind != "LOSS" || event.Data.Money.Amount != "0.00" || event.Data.Balance.Amount != "75.00" {
					t.Fatalf("LOSS contract = %+v", event)
				}
			default:
				if event.Data.Kind != "OPENING" || event.Data.Money.Amount != "100.00" || event.Data.Balance.Amount != "100.00" {
					t.Fatalf("OPENING contract = %+v", event)
				}
				openingTransactionID = event.Data.TransactionID
			}
		case "WalletBalanceChanged":
			if event.AggregateID != account.ID() || event.Data.WalletID != account.ID() || event.CausationID == "" {
				t.Fatalf("balance contract = %+v", event)
			}
			balanceCausationByTransaction[event.Data.TransactionID] = event.CausationID
			if event.Data.TransactionID == bet.ID() && (event.Data.WalletVersion != 2 ||
				event.Data.BalanceBefore.Amount != "100.00" || event.Data.BalanceAfter.Amount != "75.00") {
				t.Fatalf("BET balance contract = %+v", event)
			}
			if event.Data.WalletVersion == 1 && (event.Data.BalanceBefore.Amount != "0.00" ||
				event.Data.BalanceAfter.Amount != "100.00") {
				t.Fatalf("OPENING balance contract = %+v", event)
			}
		case "WagerTransactionRejected":
			if event.AggregateID != rejected.ID() || event.Data.TransactionID != rejected.ID() ||
				event.Data.Status != "REJECTED" || event.Data.FailureCode != "BET_INSUFFICIENT_FUNDS" ||
				event.Data.Balance.Amount != "75.00" || event.Data.Money.Amount != "100.00" {
				t.Fatalf("rejection contract = %+v", event)
			}
		case "WagerTransactionPendingReference":
			if event.AggregateID != pending.ID() || event.Data.TransactionID != pending.ID() ||
				event.Data.Status != "PENDING_REFERENCE" || event.Data.Kind != "REFUND" ||
				event.Data.ReferenceExternalTransactionID != missingReference {
				t.Fatalf("pending contract = %+v", event)
			}
		default:
			t.Fatalf("unexpected event type %q", event.EventType)
		}
		if _, err := client.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle}); err != nil {
			t.Fatal(err)
		}
	}
	for kind, want := range map[string]int{
		"WagerTransactionProcessed": 3, "WalletBalanceChanged": 2,
		"WagerTransactionRejected": 1, "WagerTransactionPendingReference": 1,
	} {
		if counts[kind] != want {
			t.Fatalf("%s count = %d, want %d", kind, counts[kind], want)
		}
	}
	if openingTransactionID == "" || len(processedEventByTransaction) != 3 ||
		processedEventByTransaction[bet.ID()] == "" || processedEventByTransaction[loss.ID()] == "" ||
		len(balanceCausationByTransaction) != 2 ||
		balanceCausationByTransaction[openingTransactionID] != processedEventByTransaction[openingTransactionID] ||
		balanceCausationByTransaction[bet.ID()] != processedEventByTransaction[bet.ID()] {
		t.Fatal("balance event causation does not match OPENING and BET processed events")
	}
	var confirmed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE correlation_id=$1 AND published_at IS NOT NULL`, correlationID).Scan(&confirmed); err != nil || confirmed != 7 {
		t.Fatalf("confirmed events = %d, error=%v, want 7", confirmed, err)
	}
}
