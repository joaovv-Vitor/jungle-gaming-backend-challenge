//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/ledger"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wallet"
)

func TestFinancialRepositoriesPersistAndRehydrateMovement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	unit := NewUnitOfWork(pool)
	wallets := NewWalletRepository()
	wagers := NewWagerRepository()
	entries := NewLedgerRepository()
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID := randomUUID(t)
	playerID := randomUUID(t)
	openingID := randomUUID(t)
	openingEntryID := randomUUID(t)
	initial := mustMoney(t, 10_000, "BRL")
	zero := mustMoney(t, 0, "BRL")

	account, err := wallet.New(walletID, playerID, initial, now)
	if err != nil {
		t.Fatal(err)
	}
	opening, err := wagering.NewOpening(openingID, walletID, playerID, initial, now)
	if err != nil {
		t.Fatal(err)
	}
	openingEntry, err := ledger.New(ledger.Params{
		ID:            openingEntryID,
		WalletID:      walletID,
		TransactionID: openingID,
		Direction:     ledger.DirectionCredit,
		Amount:        initial,
		BalanceBefore: zero,
		BalanceAfter:  initial,
		WalletVersion: 1,
		CreatedAt:     now,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := wallets.Insert(ctx, tx, account); err != nil {
			return err
		}
		if err := wagers.Insert(ctx, tx, opening); err != nil {
			return err
		}
		return entries.Insert(ctx, tx, openingEntry)
	})
	if err != nil {
		t.Fatalf("persist opening: %v", err)
	}

	providerID := "provider-" + randomUUID(t)
	externalID := "external-" + randomUUID(t)
	idempotencyKey := "idempotency-" + randomUUID(t)
	betID := randomUUID(t)
	betEntryID := randomUUID(t)
	betAmount := mustMoney(t, 2_500, "BRL")
	movementAt := now.Add(time.Second)
	payloadHash := wagering.PayloadHash(sha256.Sum256([]byte(externalID)))

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := wallets.FindByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return err
		}
		before, after, err := locked.Debit(betAmount, movementAt)
		if err != nil {
			return err
		}
		bet, err := wagering.NewExternal(wagering.ExternalParams{
			ID:                    betID,
			ProviderID:            providerID,
			ExternalTransactionID: externalID,
			IdempotencyKey:        idempotencyKey,
			PayloadHash:           payloadHash,
			WalletID:              walletID,
			PlayerID:              playerID,
			RoundID:               "round-1",
			GameID:                "game-1",
			Kind:                  wagering.KindBet,
			Amount:                betAmount,
		}, movementAt)
		if err != nil {
			return err
		}
		if err := bet.MarkProcessed(after, movementAt); err != nil {
			return err
		}
		entry, err := ledger.New(ledger.Params{
			ID:            betEntryID,
			WalletID:      walletID,
			TransactionID: betID,
			Direction:     ledger.DirectionDebit,
			Amount:        betAmount,
			BalanceBefore: before,
			BalanceAfter:  after,
			WalletVersion: locked.Version(),
			CreatedAt:     movementAt,
		})
		if err != nil {
			return err
		}
		if err := wallets.Update(ctx, tx, locked); err != nil {
			return err
		}
		if err := wagers.Insert(ctx, tx, bet); err != nil {
			return err
		}
		return entries.Insert(ctx, tx, entry)
	})
	if err != nil {
		t.Fatalf("persist bet movement: %v", err)
	}

	var stale *wallet.Wallet
	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		stale, err = wallets.FindByID(ctx, tx, walletID)
		if err != nil {
			return err
		}
		if stale.Balance().MinorUnits() != 7_500 || stale.Version() != 2 {
			return fmt.Errorf("unexpected wallet state: balance=%d version=%d", stale.Balance().MinorUnits(), stale.Version())
		}
		byID, err := wagers.FindByID(ctx, tx, betID)
		if err != nil {
			return err
		}
		byKey, err := wagers.FindByProviderIdempotencyKey(ctx, tx, providerID, idempotencyKey)
		if err != nil {
			return err
		}
		byExternalID, err := wagers.FindByProviderExternalID(ctx, tx, providerID, externalID)
		if err != nil {
			return err
		}
		if byID.ID() != betID || byKey.ID() != betID || byExternalID.ID() != betID {
			return errors.New("wager lookup returned inconsistent transaction")
		}
		if byID.Status() != wagering.StatusProcessed || byID.PayloadHash() != payloadHash {
			return errors.New("wager was not fully rehydrated")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("rehydrate financial state: %v", err)
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return wallets.Update(ctx, tx, stale)
	})
	if !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("stale Update() error = %v, want %v", err, ErrConcurrentWrite)
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := wallets.FindByID(ctx, tx, randomUUID(t))
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing FindByID() error = %v, want %v", err, ErrNotFound)
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE wallet_ledger_entries SET amount_minor=1 WHERE id=$1`, betEntryID)
		return err
	})
	if err == nil {
		t.Fatal("ledger UPDATE succeeded, want append-only trigger error")
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO wallets(id, player_id, currency, balance_minor, version, created_at, updated_at)
			VALUES ($1, $2, 'BRL', -1, 1, $3, $3)`, randomUUID(t), randomUUID(t), now)
		return err
	})
	if err == nil {
		t.Fatal("negative wallet INSERT succeeded, want check constraint error")
	}

	duplicateWallet, err := wallet.New(randomUUID(t), playerID, zero, now)
	if err != nil {
		t.Fatal(err)
	}
	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return wallets.Insert(ctx, tx, duplicateWallet)
	})
	if err == nil {
		t.Fatal("duplicate player/currency wallet INSERT succeeded, want unique constraint error")
	}
}

func TestUnitOfWorkRollsBackRepositoryWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	unit := NewUnitOfWork(pool)
	wallets := NewWalletRepository()
	id := randomUUID(t)
	account, err := wallet.New(id, randomUUID(t), mustMoney(t, 0, "BRL"), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("abort transaction")
	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := wallets.Insert(ctx, tx, account); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReadCommitted() error = %v, want %v", err, wantErr)
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := wallets.FindByID(ctx, tx, id)
		return err
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled-back wallet lookup error = %v, want %v", err, ErrNotFound)
	}
}

func integrationDatabaseURL() string {
	if value := os.Getenv("APP_DATABASE_URL"); value != "" {
		return value
	}
	return "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"
}

func mustMoney(t *testing.T, minor int64, code string) money.Money {
	t.Helper()
	currency, err := money.ParseCurrency(code)
	if err != nil {
		t.Fatal(err)
	}
	value, err := money.New(minor, currency)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func randomUUID(t *testing.T) string {
	t.Helper()
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
