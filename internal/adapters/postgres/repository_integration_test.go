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

	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
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
		if err := wagers.Insert(ctx, tx, opening, nil); err != nil {
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
		if err := wagers.Insert(ctx, tx, bet, nil); err != nil {
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

func TestPendingReferenceRequiresDurableSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	unit := NewUnitOfWork(pool)
	wallets := NewWalletRepository()
	wagers := NewWagerRepository()
	now := time.Now().UTC().Truncate(time.Microsecond)
	walletID := randomUUID(t)
	playerID := randomUUID(t)
	account, err := wallet.New(walletID, playerID, mustMoney(t, 0, "BRL"), now)
	if err != nil {
		t.Fatal(err)
	}
	hash := wagering.PayloadHash(sha256.Sum256([]byte("pending-reference")))
	pending, err := wagering.NewExternal(wagering.ExternalParams{
		ID:                             randomUUID(t),
		ProviderID:                     "provider-" + randomUUID(t),
		ExternalTransactionID:          "external-" + randomUUID(t),
		IdempotencyKey:                 "idempotency-" + randomUUID(t),
		PayloadHash:                    hash,
		WalletID:                       walletID,
		PlayerID:                       playerID,
		RoundID:                        "round-pending",
		GameID:                         "game-pending",
		Kind:                           wagering.KindRefund,
		Amount:                         mustMoney(t, 1_000, "BRL"),
		ReferenceExternalTransactionID: "missing-reference",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.MarkPendingReference(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := wallets.Insert(ctx, tx, account); err != nil {
			return err
		}
		return wagers.Insert(ctx, tx, pending, nil)
	})
	if !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("Insert(pending without schedule) error = %v, want %v", err, ErrInvalidSchedule)
	}

	schedule := &ReferenceSchedule{
		NextAttemptAt: now.Add(time.Minute),
		ExpiresAt:     now.Add(time.Hour),
	}
	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := wallets.Insert(ctx, tx, account); err != nil {
			return err
		}
		return wagers.Insert(ctx, tx, pending, schedule)
	})
	if err != nil {
		t.Fatalf("persist scheduled pending reference: %v", err)
	}
}

func TestDatabaseRejectsInvalidFinancialSemantics(t *testing.T) {
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
	initial := mustMoney(t, 10_000, "BRL")
	zero := mustMoney(t, 0, "BRL")
	account, err := wallet.New(walletID, playerID, initial, now)
	if err != nil {
		t.Fatal(err)
	}
	openingID := randomUUID(t)
	opening, err := wagering.NewOpening(openingID, walletID, playerID, initial, now)
	if err != nil {
		t.Fatal(err)
	}
	openingEntry, err := ledger.New(ledger.Params{
		ID: randomUUID(t), WalletID: walletID, TransactionID: openingID,
		Direction: ledger.DirectionCredit, Amount: initial, BalanceBefore: zero,
		BalanceAfter: initial, WalletVersion: 1, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := wallets.Insert(ctx, tx, account); err != nil {
			return err
		}
		if err := wagers.Insert(ctx, tx, opening, nil); err != nil {
			return err
		}
		return entries.Insert(ctx, tx, openingEntry)
	})
	if err != nil {
		t.Fatalf("persist opening fixture: %v", err)
	}

	amount := mustMoney(t, 2_500, "BRL")
	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := wallets.FindByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return err
		}
		before, after, err := locked.Credit(amount, now.Add(time.Second))
		if err != nil {
			return err
		}
		betID := randomUUID(t)
		bet, err := newProcessedExternal(t, wagering.KindBet, betID, walletID, playerID, amount, after, now.Add(time.Second))
		if err != nil {
			return err
		}
		entry, err := ledger.New(ledger.Params{
			ID: randomUUID(t), WalletID: walletID, TransactionID: betID,
			Direction: ledger.DirectionCredit, Amount: amount, BalanceBefore: before,
			BalanceAfter: after, WalletVersion: locked.Version(), CreatedAt: now.Add(time.Second),
		})
		if err != nil {
			return err
		}
		if err := wallets.Update(ctx, tx, locked); err != nil {
			return err
		}
		if err := wagers.Insert(ctx, tx, bet, nil); err != nil {
			return err
		}
		return entries.Insert(ctx, tx, entry)
	})
	if err == nil {
		t.Fatal("BET with CREDIT ledger committed, want financial semantics error")
	}

	err = unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		locked, err := wallets.FindByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return err
		}
		before, after, err := locked.Credit(amount, now.Add(2*time.Second))
		if err != nil {
			return err
		}
		wrongResult := mustMoney(t, after.MinorUnits()+1, "BRL")
		winID := randomUUID(t)
		win, err := newProcessedExternal(t, wagering.KindWin, winID, walletID, playerID, amount, wrongResult, now.Add(2*time.Second))
		if err != nil {
			return err
		}
		entry, err := ledger.New(ledger.Params{
			ID: randomUUID(t), WalletID: walletID, TransactionID: winID,
			Direction: ledger.DirectionCredit, Amount: amount, BalanceBefore: before,
			BalanceAfter: after, WalletVersion: locked.Version(), CreatedAt: now.Add(2 * time.Second),
		})
		if err != nil {
			return err
		}
		if err := wallets.Update(ctx, tx, locked); err != nil {
			return err
		}
		if err := wagers.Insert(ctx, tx, win, nil); err != nil {
			return err
		}
		return entries.Insert(ctx, tx, entry)
	})
	if err == nil {
		t.Fatal("wager with divergent historical result committed, want constraint error")
	}
}

func TestWalletStoreCreatesOpeningLedgerAndOutboxAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	unit := NewUnitOfWork(pool)
	store := NewWalletStore(
		unit,
		NewWalletRepository(),
		NewWagerRepository(),
		NewLedgerRepository(),
		NewOutboxRepository(),
	)
	service := applicationwallet.NewService(store)
	playerID := randomUUID(t)
	correlationID := "wallet-open-" + randomUUID(t)
	account, err := service.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatal(err)
	}

	var openings, entries, events int
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM wager_transactions WHERE wallet_id=$1 AND kind='OPENING'),
		(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1),
		(SELECT count(*) FROM outbox_events WHERE correlation_id=$2)`,
		account.ID(), correlationID).Scan(&openings, &entries, &events)
	if err != nil {
		t.Fatal(err)
	}
	if openings != 1 || entries != 1 || events != 2 {
		t.Fatalf("financial records = opening:%d ledger:%d outbox:%d, want 1/1/2", openings, entries, events)
	}
	page, err := service.ListLedger(ctx, account.ID(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].WalletVersion() != 1 || page.NextCursor != "" {
		t.Fatalf("opening ledger page = %+v", page)
	}

	_, err = service.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "duplicate-" + correlationID,
	})
	if !errors.Is(err, applicationwallet.ErrWalletAlreadyExists) {
		t.Fatalf("duplicate Open() error = %v, want %v", err, applicationwallet.ErrWalletAlreadyExists)
	}

	zeroCorrelationID := "wallet-zero-" + randomUUID(t)
	zeroAccount, err := service.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 0, "BRL"), CorrelationID: zeroCorrelationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM wager_transactions WHERE wallet_id=$1),
		(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1),
		(SELECT count(*) FROM outbox_events WHERE correlation_id=$2)`,
		zeroAccount.ID(), zeroCorrelationID).Scan(&openings, &entries, &events)
	if err != nil {
		t.Fatal(err)
	}
	if openings != 0 || entries != 0 || events != 0 {
		t.Fatalf("zero wallet records = transaction:%d ledger:%d outbox:%d, want 0/0/0", openings, entries, events)
	}
}

func newProcessedExternal(
	t *testing.T,
	kind wagering.Kind,
	id, walletID, playerID string,
	amount, result money.Money,
	now time.Time,
) (*wagering.Transaction, error) {
	t.Helper()
	externalID := "external-" + randomUUID(t)
	transaction, err := wagering.NewExternal(wagering.ExternalParams{
		ID: id, ProviderID: "provider-" + randomUUID(t), ExternalTransactionID: externalID,
		IdempotencyKey: "idempotency-" + randomUUID(t),
		PayloadHash:    wagering.PayloadHash(sha256.Sum256([]byte(externalID))),
		WalletID:       walletID, PlayerID: playerID, RoundID: "round-1", GameID: "game-1",
		Kind: kind, Amount: amount,
	}, now)
	if err != nil {
		return nil, err
	}
	if err := transaction.MarkProcessed(result, now); err != nil {
		return nil, err
	}
	return transaction, nil
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
