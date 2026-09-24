package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
)

var entryTime = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func TestNewCreditAndDebit(t *testing.T) {
	tests := []struct {
		name      string
		direction Direction
		before    string
		amount    string
		after     string
	}{
		{name: "credit", direction: DirectionCredit, before: "10.00", amount: "5.00", after: "15.00"},
		{name: "debit", direction: DirectionDebit, before: "10.00", amount: "5.00", after: "5.00"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, err := New(params(t, test.direction, test.before, test.amount, test.after))
			if err != nil {
				t.Fatal(err)
			}
			if entry.Direction() != test.direction || entry.BalanceAfter().String() != test.after {
				t.Fatalf("unexpected entry direction/balance: %s/%s", entry.Direction(), entry.BalanceAfter().String())
			}
		})
	}
}

func TestRejectsInvalidEquation(t *testing.T) {
	_, err := New(params(t, DirectionCredit, "10.00", "5.00", "14.99"))
	if !errors.Is(err, ErrBalanceEquation) {
		t.Fatalf("New() error = %v, want ErrBalanceEquation", err)
	}
	_, err = New(params(t, DirectionDebit, "5.00", "10.00", "0.00"))
	if !errors.Is(err, ErrBalanceEquation) {
		t.Fatalf("New(overdraft) error = %v, want ErrBalanceEquation", err)
	}
}

func TestRejectsZeroAndCurrencyMismatch(t *testing.T) {
	zero := params(t, DirectionCredit, "10.00", "0.00", "10.00")
	if _, err := New(zero); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("New(zero) error = %v", err)
	}

	mismatch := params(t, DirectionCredit, "10.00", "1.00", "11.00")
	mismatch.Amount = parseLedgerMoney(t, "1.00", "USD")
	if _, err := New(mismatch); !errors.Is(err, ErrBalanceEquation) || !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("New(currency mismatch) error = %v", err)
	}
}

func TestRehydrateValidatesPersistedState(t *testing.T) {
	state := params(t, DirectionDebit, "10.00", "1.00", "9.00")
	entry, err := Rehydrate(state)
	if err != nil || entry.WalletVersion() != 2 {
		t.Fatalf("Rehydrate() entry=%v error=%v", entry, err)
	}
	state.WalletVersion = 0
	if _, err := Rehydrate(state); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("Rehydrate(version) error = %v", err)
	}
}

func params(t *testing.T, direction Direction, before, amount, after string) Params {
	t.Helper()
	return Params{
		ID: "entry-1", WalletID: "wallet-1", TransactionID: "transaction-1",
		Direction: direction, Amount: parseLedgerMoney(t, amount, "BRL"),
		BalanceBefore: parseLedgerMoney(t, before, "BRL"), BalanceAfter: parseLedgerMoney(t, after, "BRL"),
		WalletVersion: 2, CreatedAt: entryTime,
	}
}

func parseLedgerMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
