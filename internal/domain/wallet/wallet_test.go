package wallet

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
)

var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func TestNewWalletKeepsInitialVersion(t *testing.T) {
	wallet, err := New("wallet-1", "player-1", parseMoney(t, "100.00", "BRL"), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Version() != 1 || wallet.Balance().String() != "100.00" {
		t.Fatalf("wallet version/balance = %d/%s", wallet.Version(), wallet.Balance().String())
	}
}

func TestCreditAndDebitIncrementVersion(t *testing.T) {
	wallet, _ := New("wallet-1", "player-1", parseMoney(t, "100.00", "BRL"), testNow)

	before, after, err := wallet.Debit(parseMoney(t, "25.00", "BRL"), testNow.Add(time.Second))
	if err != nil || before.String() != "100.00" || after.String() != "75.00" || wallet.Version() != 2 {
		t.Fatalf("Debit() before=%s after=%s version=%d err=%v", before.String(), after.String(), wallet.Version(), err)
	}
	before, after, err = wallet.Credit(parseMoney(t, "5.00", "BRL"), testNow.Add(2*time.Second))
	if err != nil || before.String() != "75.00" || after.String() != "80.00" || wallet.Version() != 3 {
		t.Fatalf("Credit() before=%s after=%s version=%d err=%v", before.String(), after.String(), wallet.Version(), err)
	}
}

func TestDebitRejectsInsufficientFundsWithoutMutation(t *testing.T) {
	wallet, _ := New("wallet-1", "player-1", parseMoney(t, "10.00", "BRL"), testNow)
	_, _, err := wallet.Debit(parseMoney(t, "10.01", "BRL"), testNow.Add(time.Second))
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("Debit() error = %v", err)
	}
	if wallet.Balance().String() != "10.00" || wallet.Version() != 1 || !wallet.UpdatedAt().Equal(testNow) {
		t.Fatalf("wallet mutated after rejected debit: balance=%s version=%d", wallet.Balance().String(), wallet.Version())
	}
}

func TestMovementRejectsZeroNegativeAndCurrencyMismatch(t *testing.T) {
	wallet, _ := New("wallet-1", "player-1", parseMoney(t, "10.00", "BRL"), testNow)

	for _, amount := range []money.Money{parseMoney(t, "0.00", "BRL"), parseMoney(t, "-1.00", "BRL")} {
		_, _, err := wallet.Credit(amount, testNow.Add(time.Second))
		if !errors.Is(err, ErrNonPositiveAmount) {
			t.Fatalf("Credit(%s) error = %v", amount.String(), err)
		}
	}
	_, _, err := wallet.Credit(parseMoney(t, "1.00", "USD"), testNow.Add(time.Second))
	if !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Fatalf("Credit(currency mismatch) error = %v", err)
	}
}

func TestMovementDetectsAmountAndVersionOverflow(t *testing.T) {
	currency, _ := money.ParseCurrency("BRL")
	maximum, _ := money.New(math.MaxInt64, currency)
	wallet, _ := New("wallet-1", "player-1", maximum, testNow)
	_, _, err := wallet.Credit(parseMoney(t, "0.01", "BRL"), testNow.Add(time.Second))
	if !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("Credit() error = %v, want money.ErrOverflow", err)
	}

	wallet, _ = Rehydrate(Rehydration{
		ID: "wallet-1", PlayerID: "player-1", Balance: parseMoney(t, "1.00", "BRL"),
		Version: math.MaxInt64, CreatedAt: testNow, UpdatedAt: testNow,
	})
	_, _, err = wallet.Debit(parseMoney(t, "0.01", "BRL"), testNow.Add(time.Second))
	if !errors.Is(err, ErrVersionOverflow) {
		t.Fatalf("Debit() error = %v, want ErrVersionOverflow", err)
	}
}

func TestRehydrateValidatesState(t *testing.T) {
	valid := Rehydration{
		ID: "wallet-1", PlayerID: "player-1", Balance: parseMoney(t, "1.00", "BRL"),
		Version: 3, CreatedAt: testNow, UpdatedAt: testNow.Add(time.Second),
	}
	wallet, err := Rehydrate(valid)
	if err != nil || wallet.Version() != 3 {
		t.Fatalf("Rehydrate() wallet=%v error=%v", wallet, err)
	}

	valid.Balance = parseMoney(t, "-0.01", "BRL")
	if _, err := Rehydrate(valid); !errors.Is(err, ErrInvalidWallet) {
		t.Fatalf("Rehydrate(negative) error = %v", err)
	}
}

func parseMoney(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	value, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
