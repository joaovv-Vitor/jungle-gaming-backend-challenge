package wagering

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
)

var transactionTime = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func TestNewExternalRules(t *testing.T) {
	tests := []struct {
		name      string
		kind      Kind
		amount    string
		reference string
		wantError error
	}{
		{name: "bet", kind: KindBet, amount: "10.00"},
		{name: "win without reference", kind: KindWin, amount: "20.00"},
		{name: "win with reference", kind: KindWin, amount: "20.00", reference: "bet-1"},
		{name: "loss", kind: KindLoss, amount: "0.00"},
		{name: "refund", kind: KindRefund, amount: "10.00", reference: "bet-1"},
		{name: "rollback", kind: KindRollback, amount: "10.00", reference: "bet-1"},
		{name: "external opening", kind: KindOpening, amount: "10.00", wantError: ErrInvalidKind},
		{name: "loss positive", kind: KindLoss, amount: "1.00", wantError: ErrInvalidTransaction},
		{name: "bet zero", kind: KindBet, amount: "0.00", wantError: ErrInvalidTransaction},
		{name: "refund without reference", kind: KindRefund, amount: "1.00", wantError: ErrInvalidReference},
		{name: "bet with reference", kind: KindBet, amount: "1.00", reference: "bet-1", wantError: ErrInvalidReference},
		{name: "self reference", kind: KindRollback, amount: "1.00", reference: "external-1", wantError: ErrInvalidReference},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := validExternalParams(t, test.kind, test.amount)
			params.ReferenceExternalTransactionID = test.reference
			transaction, err := NewExternal(params, transactionTime)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("NewExternal() error = %v, want %v", err, test.wantError)
				}
				return
			}
			if err != nil || transaction.Status() != StatusPending {
				t.Fatalf("NewExternal() transaction=%v error=%v", transaction, err)
			}
		})
	}
}

func TestOpeningIsInternalAndProcessed(t *testing.T) {
	opening, err := NewOpening("opening-1", "wallet-1", "player-1", transactionMoney(t, "100.00"), transactionTime)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := opening.ResultBalance()
	if opening.Origin() != OriginInternal || opening.Kind() != KindOpening || opening.Status() != StatusProcessed || !ok || result.String() != "100.00" {
		t.Fatalf("unexpected opening: origin=%s kind=%s status=%s result=%s/%v", opening.Origin(), opening.Kind(), opening.Status(), result.String(), ok)
	}
}

func TestTransactionStateMachine(t *testing.T) {
	params := validExternalParams(t, KindRefund, "10.00")
	params.ReferenceExternalTransactionID = "bet-1"
	transaction, _ := NewExternal(params, transactionTime)

	if err := transaction.MarkPendingReference(transactionTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := transaction.MarkPendingReference(transactionTime.Add(2 * time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("second MarkPendingReference() error = %v", err)
	}
	if err := transaction.ResolveReference("transaction-bet-1", transactionTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := transaction.MarkProcessed(transactionMoney(t, "110.00"), transactionTime.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if transaction.Status() != StatusProcessed || transaction.ReferenceTransactionID() != "transaction-bet-1" {
		t.Fatalf("unexpected final state: %s/%s", transaction.Status(), transaction.ReferenceTransactionID())
	}
	if err := transaction.Reject(FailureAlreadyReversed, transactionMoney(t, "110.00"), transactionTime.Add(4*time.Second)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("terminal Reject() error = %v", err)
	}
}

func TestReferencedTransactionCannotProcessBeforeResolution(t *testing.T) {
	params := validExternalParams(t, KindWin, "10.00")
	params.ReferenceExternalTransactionID = "bet-1"
	transaction, _ := NewExternal(params, transactionTime)
	if err := transaction.MarkProcessed(transactionMoney(t, "110.00"), transactionTime.Add(time.Second)); !errors.Is(err, ErrInvalidReference) {
		t.Fatalf("MarkProcessed() error = %v, want ErrInvalidReference", err)
	}
}

func TestRejectAndFailPersistClassifiableResults(t *testing.T) {
	rejected, _ := NewExternal(validExternalParams(t, KindBet, "80.00"), transactionTime)
	if err := rejected.Reject(FailureBetInsufficientFunds, transactionMoney(t, "20.00"), transactionTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	result, ok := rejected.ResultBalance()
	if rejected.Status() != StatusRejected || rejected.FailureCode() != FailureBetInsufficientFunds || !ok || result.String() != "20.00" {
		t.Fatalf("unexpected rejection result")
	}

	failed, _ := NewExternal(validExternalParams(t, KindBet, "1.00"), transactionTime)
	if err := failed.Fail(FailureInfrastructurePermanent, transactionTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := failed.ResultBalance(); failed.Status() != StatusFailed || ok {
		t.Fatalf("FAILED must not contain a financial result")
	}
}

func TestTransitionRejectsCurrencyAndTimeMismatch(t *testing.T) {
	transaction, _ := NewExternal(validExternalParams(t, KindBet, "1.00"), transactionTime)
	usd, _ := money.Parse("10.00", "USD")
	if err := transaction.MarkProcessed(usd, transactionTime.Add(time.Second)); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("MarkProcessed(currency) error = %v", err)
	}
	if err := transaction.MarkProcessed(transactionMoney(t, "10.00"), transactionTime.Add(-time.Second)); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("MarkProcessed(time) error = %v", err)
	}
}

func TestRehydrateRejectsInconsistentTerminalState(t *testing.T) {
	params := validExternalParams(t, KindBet, "1.00")
	state := externalRehydration(params)
	state.Status = StatusProcessed
	if _, err := Rehydrate(state); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("Rehydrate(processed without result) error = %v", err)
	}

	result := transactionMoney(t, "5.00")
	state.ResultBalance = &result
	transaction, err := Rehydrate(state)
	if err != nil || transaction.Status() != StatusProcessed {
		t.Fatalf("Rehydrate(valid) transaction=%v error=%v", transaction, err)
	}
}

func validExternalParams(t *testing.T, kind Kind, amount string) ExternalParams {
	t.Helper()
	hash := sha256.Sum256([]byte("canonical payload"))
	return ExternalParams{
		ID:                    "transaction-1",
		ProviderID:            "provider-a",
		ExternalTransactionID: "external-1",
		IdempotencyKey:        "provider-a:external-1",
		PayloadHash:           hash,
		WalletID:              "wallet-1",
		PlayerID:              "player-1",
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  kind,
		Amount:                transactionMoney(t, amount),
	}
}

func externalRehydration(params ExternalParams) Rehydration {
	return Rehydration{
		ID: params.ID, Origin: OriginExternal, ProviderID: params.ProviderID,
		ExternalTransactionID: params.ExternalTransactionID, IdempotencyKey: params.IdempotencyKey,
		PayloadHash: params.PayloadHash, WalletID: params.WalletID, PlayerID: params.PlayerID,
		RoundID: params.RoundID, GameID: params.GameID, Kind: params.Kind, Amount: params.Amount,
		ReferenceExternalTransactionID: params.ReferenceExternalTransactionID,
		Status:                         StatusPending, CreatedAt: transactionTime, UpdatedAt: transactionTime,
	}
}

func transactionMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return value
}
