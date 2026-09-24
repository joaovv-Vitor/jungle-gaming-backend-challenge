package event

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
)

var eventTime = time.Date(2026, 9, 22, 12, 0, 0, 123000000, time.FixedZone("BRT", -3*60*60))

func TestProcessedEventDefinesEnvelopeAndImmutableSnapshot(t *testing.T) {
	event, err := NewWagerTransactionProcessed(transactionMetadata(), ProcessedInput{
		TransactionID: "transaction-1", ProviderID: "provider-a", Kind: wagering.KindBet,
		Money: eventMoney(t, "25.00", "BRL"), Balance: eventMoney(t, "75.00", "BRL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Type() != TypeWagerTransactionProcessed || event.Version() != ContractVersion || event.OccurredAt().Location() != time.UTC {
		t.Fatalf("unexpected envelope type/version/timezone")
	}

	copyOfData := event.Data()
	copyOfData.TransactionID = "changed"
	if event.Data().TransactionID != "transaction-1" {
		t.Fatal("event data changed through returned copy")
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, expected := range []string{
		`"eventType":"WagerTransactionProcessed"`, `"version":1`,
		`"occurredAt":"2026-09-22T15:00:00.123Z"`, `"amount":"25.00"`, `"currency":"BRL"`,
	} {
		if !strings.Contains(jsonText, expected) {
			t.Fatalf("event JSON %s does not contain %s", jsonText, expected)
		}
	}
}

func TestOpeningProcessedEventOmitsProvider(t *testing.T) {
	metadata := transactionMetadata()
	opening, err := NewWagerTransactionProcessed(metadata, ProcessedInput{
		TransactionID: "transaction-1", Kind: wagering.KindOpening,
		Money: eventMoney(t, "100.00", "BRL"), Balance: eventMoney(t, "100.00", "BRL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(opening)
	if strings.Contains(string(encoded), "providerId") {
		t.Fatalf("opening event contains providerId: %s", encoded)
	}
}

func TestRejectedAndPendingEventsSetDomainStatuses(t *testing.T) {
	rejected, err := NewWagerTransactionRejected(transactionMetadata(), RejectedInput{
		TransactionID: "transaction-1", ProviderID: "provider-a", Kind: wagering.KindBet,
		FailureCode: wagering.FailureBetInsufficientFunds,
		Money:       eventMoney(t, "80.00", "BRL"), Balance: eventMoney(t, "20.00", "BRL"),
	})
	if err != nil || rejected.Data().Status != wagering.StatusRejected {
		t.Fatalf("rejected event=%v error=%v", rejected, err)
	}

	pending, err := NewWagerTransactionPendingReference(transactionMetadata(), PendingReferenceInput{
		TransactionID: "transaction-1", ProviderID: "provider-a", Kind: wagering.KindRefund,
		ReferenceExternalTransactionID: "bet-1",
	})
	if err != nil || pending.Data().Status != wagering.StatusPendingReference {
		t.Fatalf("pending event=%v error=%v", pending, err)
	}
}

func TestWalletBalanceChangedValidatesEquation(t *testing.T) {
	metadata := Metadata{
		EventID: "event-1", AggregateID: "wallet-1", CorrelationID: "correlation-1", OccurredAt: eventTime,
	}
	data := WalletBalanceChangedData{
		WalletID: "wallet-1", TransactionID: "transaction-1", Direction: ledger.DirectionDebit,
		Money: eventMoney(t, "25.00", "BRL"), BalanceBefore: eventMoney(t, "100.00", "BRL"),
		BalanceAfter: eventMoney(t, "75.00", "BRL"), WalletVersion: 2,
	}
	event, err := NewWalletBalanceChanged(metadata, data)
	if err != nil || event.Type() != TypeWalletBalanceChanged {
		t.Fatalf("balance event=%v error=%v", event, err)
	}

	data.BalanceAfter = eventMoney(t, "74.99", "BRL")
	if _, err := NewWalletBalanceChanged(metadata, data); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid equation error = %v", err)
	}
}

func TestEventRejectsCurrencyMismatchAndInvalidMetadata(t *testing.T) {
	_, err := NewWagerTransactionProcessed(transactionMetadata(), ProcessedInput{
		TransactionID: "transaction-1", ProviderID: "provider-a", Kind: wagering.KindWin,
		Money: eventMoney(t, "1.00", "USD"), Balance: eventMoney(t, "10.00", "BRL"),
	})
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("currency mismatch error = %v", err)
	}

	metadata := transactionMetadata()
	metadata.EventID = ""
	_, err = NewWagerTransactionProcessed(metadata, ProcessedInput{
		TransactionID: "transaction-1", ProviderID: "provider-a", Kind: wagering.KindLoss,
		Money: eventMoney(t, "0.00", "BRL"), Balance: eventMoney(t, "10.00", "BRL"),
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid metadata error = %v", err)
	}
}

func transactionMetadata() Metadata {
	return Metadata{
		EventID: "event-1", AggregateID: "transaction-1", CorrelationID: "correlation-1",
		CausationID: "message-1", OccurredAt: eventTime,
	}
}

func eventMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
