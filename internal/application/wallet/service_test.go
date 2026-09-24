package wallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	walletdomain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wallet"
)

type walletStoreFake struct {
	created Creation
	err     error
}

func (s *walletStoreFake) Create(_ context.Context, creation Creation) error {
	s.created = creation
	return s.err
}

func (s *walletStoreFake) FindByID(context.Context, string) (*walletdomain.Wallet, error) {
	return nil, ErrWalletNotFound
}

func (s *walletStoreFake) ListLedger(context.Context, string, *LedgerCursor, int) ([]*ledger.Entry, error) {
	return nil, nil
}

func TestOpenPositiveWalletBuildsAtomicFinancialCreation(t *testing.T) {
	store := &walletStoreFake{}
	service := deterministicService(store)
	initial, _ := money.Parse("100.00", "BRL")

	account, err := service.Open(context.Background(), OpenInput{
		PlayerID: "10000000-0000-4000-8000-000000000001", Initial: initial, CorrelationID: "correlation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	creation := store.created
	if creation.Wallet != account || creation.Opening == nil || creation.Ledger == nil || len(creation.Events) != 2 {
		t.Fatalf("incomplete financial creation: %+v", creation)
	}
	if creation.Opening.Kind() != wagering.KindOpening || creation.Opening.Status() != wagering.StatusProcessed {
		t.Fatalf("opening = %s/%s", creation.Opening.Kind(), creation.Opening.Status())
	}
	if creation.Ledger.BalanceBefore().MinorUnits() != 0 || creation.Ledger.BalanceAfter().MinorUnits() != 10_000 {
		t.Fatalf("ledger balances = %d/%d", creation.Ledger.BalanceBefore().MinorUnits(), creation.Ledger.BalanceAfter().MinorUnits())
	}
}

func TestOpenZeroWalletDoesNotCreateFinancialRecords(t *testing.T) {
	store := &walletStoreFake{}
	service := deterministicService(store)
	initial, _ := money.Parse("0.00", "BRL")

	if _, err := service.Open(context.Background(), OpenInput{
		PlayerID: "10000000-0000-4000-8000-000000000001", Initial: initial, CorrelationID: "correlation-1",
	}); err != nil {
		t.Fatal(err)
	}
	if store.created.Opening != nil || store.created.Ledger != nil || len(store.created.Events) != 0 {
		t.Fatalf("zero wallet created financial records: %+v", store.created)
	}
}

func TestOpenPropagatesPersistentConflict(t *testing.T) {
	store := &walletStoreFake{err: ErrWalletAlreadyExists}
	service := deterministicService(store)
	initial, _ := money.Parse("0.00", "BRL")
	_, err := service.Open(context.Background(), OpenInput{
		PlayerID: "10000000-0000-4000-8000-000000000001", Initial: initial, CorrelationID: "correlation-1",
	})
	if !errors.Is(err, ErrWalletAlreadyExists) {
		t.Fatalf("Open() error = %v, want %v", err, ErrWalletAlreadyExists)
	}
}

func deterministicService(store Store) *Service {
	ids := []string{
		"20000000-0000-4000-8000-000000000001",
		"20000000-0000-4000-8000-000000000002",
		"20000000-0000-4000-8000-000000000003",
		"20000000-0000-4000-8000-000000000004",
		"20000000-0000-4000-8000-000000000005",
	}
	index := 0
	service := NewService(store)
	service.now = func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) }
	service.newID = func() (string, error) {
		value := ids[index]
		index++
		return value, nil
	}
	return service
}
