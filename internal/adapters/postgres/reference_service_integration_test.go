//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	applicationreference "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/reference"
	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	domain "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
	domainwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

func TestReferenceWorkerReschedulesAndCompletesAfterReferenceArrives(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, wagers, worker := referenceServices(t, ctx)
	defer pool.Close()
	account, playerID := referenceWallet(t, ctx, wallets)
	referenceID := "late-bet-" + randomUUID(t)
	input := wagerInput(t, account.ID(), playerID, "refund-"+randomUUID(t), domain.KindRefund, mustMoney(t, 2_500, "BRL"), referenceID)
	pending, err := wagers.Submit(ctx, input)
	if err != nil || pending.Transaction.Status() != domain.StatusPendingReference {
		t.Fatalf("submit pending = %+v, error=%v", pending, err)
	}
	forceReferenceDue(t, ctx, pool, pending.Transaction.ID())
	if outcome, err := worker.ProcessOne(ctx); err != nil || outcome != applicationreference.OutcomeRescheduled {
		t.Fatalf("missing reference = %s, error=%v", outcome, err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT reference_attempts FROM wager_transactions WHERE id=$1`, pending.Transaction.ID()).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("reference attempts = %d, error=%v, want 1", attempts, err)
	}
	bet := wagerInput(t, account.ID(), playerID, referenceID, domain.KindBet, mustMoney(t, 2_500, "BRL"), "")
	if _, err := wagers.Submit(ctx, bet); err != nil {
		t.Fatal(err)
	}
	// The stored deadline limits how long to wait, not a valid reference that
	// has arrived before the worker's final recheck.
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET expires_at=clock_timestamp()-interval '1 second', next_attempt_at='2000-01-01' WHERE id=$1`, pending.Transaction.ID()); err != nil {
		t.Fatal(err)
	}
	if outcome, err := worker.ProcessOne(ctx); err != nil || outcome != applicationreference.OutcomeCompleted {
		t.Fatalf("resolved reference = %s, error=%v", outcome, err)
	}
	assertReferenceState(t, ctx, pool, wallets, account.ID(), pending.Transaction.ID(), domain.StatusProcessed, "", 10_000, 3, 2)
}

func TestReferenceWorkerReschedulesWhileReferenceIsPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, wagers, worker := referenceServices(t, ctx)
	defer pool.Close()
	account, playerID := referenceWallet(t, ctx, wallets)
	parentID := "pending-win-" + randomUUID(t)
	parent := wagerInput(t, account.ID(), playerID, parentID, domain.KindWin, mustMoney(t, 2_500, "BRL"), "missing-bet-"+randomUUID(t))
	parentResult, err := wagers.Submit(ctx, parent)
	if err != nil || parentResult.Transaction.Status() != domain.StatusPendingReference {
		t.Fatalf("submit pending parent = %+v, error=%v", parentResult, err)
	}
	dependent := wagerInput(t, account.ID(), playerID, "rollback-"+randomUUID(t), domain.KindRollback, mustMoney(t, 2_500, "BRL"), parentID)
	result, err := wagers.Submit(ctx, dependent)
	if err != nil || result.Transaction.Status() != domain.StatusPendingReference {
		t.Fatalf("submit pending dependent = %+v, error=%v", result, err)
	}
	forceReferenceDue(t, ctx, pool, result.Transaction.ID())
	if outcome, err := worker.ProcessOne(ctx); err != nil || outcome != applicationreference.OutcomeRescheduled {
		t.Fatalf("pending reference = %s, error=%v", outcome, err)
	}
	assertReferenceState(t, ctx, pool, wallets, account.ID(), result.Transaction.ID(), domain.StatusPendingReference, "", 10_000, 1, 1)
	for _, transactionID := range []string{result.Transaction.ID(), parentResult.Transaction.ID()} {
		if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET expires_at=clock_timestamp()-interval '1 second', next_attempt_at='2000-01-01' WHERE id=$1`, transactionID); err != nil {
			t.Fatal(err)
		}
		if outcome, err := worker.ProcessOne(ctx); err != nil || outcome != applicationreference.OutcomeCompleted {
			t.Fatalf("expire pending fixture = %s, error=%v", outcome, err)
		}
	}
}

func TestReferenceWorkerRejectsExpiredAndIncompatibleReferences(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		kind        domain.Kind
		amountMinor int64
		failure     domain.FailureCode
		walletMinor int64
		ledgerRows  int
	}{
		{name: "expired", failure: domain.FailureReferenceNotFound, walletMinor: 10_000, ledgerRows: 1},
		{name: "incompatible", kind: domain.KindWin, failure: domain.FailureReferenceTypeNotAllowed, walletMinor: 12_500, ledgerRows: 2},
		{name: "rejected", kind: domain.KindBet, amountMinor: 20_000, failure: domain.FailureReferenceNotProcessed, walletMinor: 10_000, ledgerRows: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			pool, wallets, wagers, worker := referenceServices(t, ctx)
			defer pool.Close()
			account, playerID := referenceWallet(t, ctx, wallets)
			referenceID := "reference-" + randomUUID(t)
			input := wagerInput(t, account.ID(), playerID, "refund-"+randomUUID(t), domain.KindRefund, mustMoney(t, 2_500, "BRL"), referenceID)
			pending, err := wagers.Submit(ctx, input)
			if err != nil || pending.Transaction.Status() != domain.StatusPendingReference {
				t.Fatalf("submit pending = %+v, error=%v", pending, err)
			}
			if scenario.kind != "" {
				amount := scenario.amountMinor
				if amount == 0 {
					amount = 2_500
				}
				other := wagerInput(t, account.ID(), playerID, referenceID, scenario.kind, mustMoney(t, amount, "BRL"), "")
				if result, err := wagers.Submit(ctx, other); err != nil {
					t.Fatal(err)
				} else if scenario.name == "rejected" && result.Transaction.Status() != domain.StatusRejected {
					t.Fatalf("reference status = %s, want REJECTED", result.Transaction.Status())
				}
				forceReferenceDue(t, ctx, pool, pending.Transaction.ID())
			} else {
				if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET expires_at=clock_timestamp()-interval '1 second', next_attempt_at='2000-01-01' WHERE id=$1`, pending.Transaction.ID()); err != nil {
					t.Fatal(err)
				}
			}
			if outcome, err := worker.ProcessOne(ctx); err != nil || outcome != applicationreference.OutcomeCompleted {
				t.Fatalf("finalize reference = %s, error=%v", outcome, err)
			}
			assertReferenceState(t, ctx, pool, wallets, account.ID(), pending.Transaction.ID(), domain.StatusRejected, scenario.failure, scenario.walletMinor, scenario.ledgerRows, 2)
		})
	}
}

func TestReferenceLeaseExpiresAndStaleTokenCannotChangeSchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, secondPool := isolatedSchedulerPools(t, ctx)
	wallets, wagers := financialServicesOnPool(pool)
	account, playerID := referenceWallet(t, ctx, wallets)
	input := wagerInput(t, account.ID(), playerID, "pending-"+randomUUID(t), domain.KindRefund, mustMoney(t, 500, "BRL"), "missing-"+randomUUID(t))
	pending, err := wagers.Submit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	forceReferenceDue(t, ctx, pool, pending.Transaction.ID())
	store := referenceStoreForPool(pool)
	otherStore := referenceStoreForPool(secondPool)
	first, err := store.Claim(ctx, 30*time.Second)
	if err != nil || first == nil {
		t.Fatalf("first claim = %+v, error=%v", first, err)
	}
	var activeToken string
	var scheduled, lockedUntil, expiresAt time.Time
	if err := secondPool.QueryRow(ctx, `SELECT lease_token::text, next_attempt_at, locked_until, expires_at
		FROM wager_transactions WHERE id=$1`, first.TransactionID).
		Scan(&activeToken, &scheduled, &lockedUntil, &expiresAt); err != nil || activeToken != first.Token {
		t.Fatalf("live lease token = %q, error=%v, want %q", activeToken, err, first.Token)
	}
	if !scheduled.Equal(lockedUntil) && !scheduled.Equal(expiresAt) {
		t.Fatalf("claimed reference schedule = %s, lease = %s, expiry = %s", scheduled, lockedUntil, expiresAt)
	}
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET locked_until=clock_timestamp()-interval '1 second',
		next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.TransactionID); err != nil {
		t.Fatal(err)
	}
	second, err := otherStore.Claim(ctx, 30*time.Second)
	if err != nil || second == nil || second.Token == first.Token {
		t.Fatalf("new claim = %+v, error=%v", second, err)
	}
	if err := store.WithinTransaction(ctx, func(session applicationreference.Session) error {
		if _, err := session.LockWallet(ctx, first.WalletID); err != nil {
			return err
		}
		stale, err := session.LockPending(ctx, *first)
		if err != nil {
			return err
		}
		if stale != nil {
			t.Fatal("stale lease returned pending transaction")
		}
		if err := session.Reschedule(ctx, *first, time.Now().Add(time.Second)); err != applicationreference.ErrInvalidClaim {
			t.Fatalf("stale lease reschedule error = %v, want ErrInvalidClaim", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := otherStore.WithinTransaction(ctx, func(session applicationreference.Session) error {
		if _, err := session.LockWallet(ctx, second.WalletID); err != nil {
			return err
		}
		current, err := session.LockPending(ctx, *second)
		if err != nil || current == nil {
			t.Fatalf("current lease = %+v, error=%v", current, err)
		}
		return session.Reschedule(ctx, *second, current.Now.Add(time.Second))
	}); err != nil {
		t.Fatal(err)
	}
	var attempts int
	if err := pool.QueryRow(ctx, `SELECT reference_attempts FROM wager_transactions WHERE id=$1`, pending.Transaction.ID()).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts = %d, error=%v, want 1", attempts, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET expires_at=clock_timestamp()-interval '1 second', next_attempt_at='2000-01-01' WHERE id=$1`, pending.Transaction.ID()); err != nil {
		t.Fatal(err)
	}
	worker := applicationreference.NewService(otherStore, wagers, config.Config{ReferenceLease: 30 * time.Second})
	if outcome, err := worker.ProcessOne(ctx); err != nil || outcome != applicationreference.OutcomeCompleted {
		t.Fatalf("finish lease fixture = %s, error=%v", outcome, err)
	}
}

func referenceServices(t *testing.T, ctx context.Context) (*pgxpool.Pool, *applicationwallet.Service, *applicationwagering.Service, *applicationreference.Service) {
	t.Helper()
	pool, wallets, wagers := financialServices(t, ctx)
	return pool, wallets, wagers, applicationreference.NewService(referenceStoreForPool(pool), wagers, config.Config{ReferenceLease: 30 * time.Second})
}

func referenceStoreForPool(pool *pgxpool.Pool) *ReferenceStore {
	return NewReferenceStore(NewUnitOfWork(pool), NewWalletRepository(), NewWagerRepository(), NewLedgerRepository(), NewOutboxRepository())
}

func referenceWallet(t *testing.T, ctx context.Context, wallets *applicationwallet.Service) (*domainwallet.Wallet, string) {
	t.Helper()
	playerID := randomUUID(t)
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	return account, playerID
}

func forceReferenceDue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, transactionID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET next_attempt_at='2000-01-01' WHERE id=$1`, transactionID); err != nil {
		t.Fatal(err)
	}
}

func assertReferenceState(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, wallets *applicationwallet.Service,
	walletID, transactionID string, status domain.Status, failure domain.FailureCode,
	balance int64, ledgerRows, eventRows int,
) {
	t.Helper()
	account, err := wallets.FindByID(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Balance().MinorUnits() != balance {
		t.Fatalf("wallet balance = %d, want %d", account.Balance().MinorUnits(), balance)
	}
	var actualStatus, actualFailure string
	var actualLedger, actualEvents int
	if err := pool.QueryRow(ctx, `SELECT status, coalesce(failure_code,'') FROM wager_transactions WHERE id=$1`, transactionID).Scan(&actualStatus, &actualFailure); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&actualLedger); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1`, transactionID).Scan(&actualEvents); err != nil {
		t.Fatal(err)
	}
	if actualStatus != string(status) || actualFailure != string(failure) || actualLedger != ledgerRows || actualEvents != eventRows {
		t.Fatalf("state = %s/%s ledger=%d events=%d, want %s/%s ledger=%d events=%d", actualStatus, actualFailure, actualLedger, actualEvents, status, failure, ledgerRows, eventRows)
	}
}
