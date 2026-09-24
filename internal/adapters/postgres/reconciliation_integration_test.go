//go:build integration

package postgres

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	applicationreconciliation "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reconciliation"
	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

func TestReconciliationMatchesOpeningAndFinancialLedger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, wagers := financialServices(t, ctx)
	defer pool.Close()
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "reconcile-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := wagerInput(t, account.ID(), account.PlayerID(), "bet-"+randomUUID(t), domain.KindBet, mustMoney(t, 2_500, "BRL"), "")
	if _, err := wagers.Submit(ctx, input); err != nil {
		t.Fatal(err)
	}
	service := applicationreconciliation.NewService(NewReconciliationStore(NewUnitOfWork(pool)), metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := service.Reconcile(ctx, account.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Consistent || result.StoredBalance.String() != "75.00" ||
		result.CalculatedBalance.String() != "75.00" || result.Difference.String() != "0.00" || result.CheckedEntries != 2 {
		t.Fatalf("reconciliation = %+v", result)
	}
	zero, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 0, "BRL"), CorrelationID: "reconcile-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err = service.Reconcile(ctx, zero.ID())
	if err != nil || !result.Consistent || result.CheckedEntries != 0 || result.CalculatedBalance.String() != "0.00" {
		t.Fatalf("zero wallet reconciliation = %+v, error=%v", result, err)
	}
}

func TestReconciliationDetectsIsolatedDatabaseDivergence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, _ := financialServices(t, ctx)
	defer pool.Close()
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "mismatch-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	adminURL := os.Getenv("APP_DATABASE_ADMIN_URL")
	if adminURL == "" {
		adminURL = "postgres://wager_admin:wager_admin_local@localhost:5432/wagering?sslmode=disable"
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	setBalance := func(parent context.Context, minor int64) error {
		tx, err := adminPool.Begin(parent)
		if err != nil {
			return err
		}
		defer tx.Rollback(parent)
		if _, err := tx.Exec(parent, `SET LOCAL session_replication_role=replica`); err != nil {
			return err
		}
		if _, err := tx.Exec(parent, `UPDATE wallets SET balance_minor=$1 WHERE id=$2`, minor, account.ID()); err != nil {
			return err
		}
		return tx.Commit(parent)
	}
	if err := setBalance(ctx, 9_900); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if restored {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := setBalance(cleanupCtx, 10_000); err != nil {
			t.Errorf("restore isolated wallet fixture: %v", err)
		}
	}()
	service := applicationreconciliation.NewService(NewReconciliationStore(NewUnitOfWork(pool)), metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := service.Reconcile(ctx, account.ID())
	if err != nil || result.Consistent || result.StoredBalance.String() != "99.00" ||
		result.CalculatedBalance.String() != "100.00" || result.Difference.String() != "-1.00" {
		t.Fatalf("corrupted wallet reconciliation = %+v, error=%v", result, err)
	}
	if err := setBalance(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	restored = true
	result, err = service.Reconcile(ctx, account.ID())
	if err != nil || !result.Consistent {
		t.Fatalf("restored wallet reconciliation = %+v, error=%v", result, err)
	}
}

func TestReconciliationSeesOneCommittedSnapshotDuringFinancialWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, wagers := financialServices(t, ctx)
	defer pool.Close()
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "snapshot-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewWagerStore(NewUnitOfWork(pool), NewWalletRepository(), NewWagerRepository(), NewLedgerRepository(), NewOutboxRepository())
	input := wagerInput(t, account.ID(), account.PlayerID(), "bet-"+randomUUID(t), domain.KindBet, mustMoney(t, 2_500, "BRL"), "")
	ready := make(chan error, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- store.WithinTransaction(ctx, func(session applicationwagering.Session) error {
			_, err := wagers.SubmitInSession(ctx, session, input)
			ready <- err
			if err != nil {
				return err
			}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	service := applicationreconciliation.NewService(NewReconciliationStore(NewUnitOfWork(pool)), metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	before, err := service.Reconcile(ctx, account.ID())
	if err != nil || !before.Consistent || before.StoredBalance.String() != "100.00" || before.CheckedEntries != 1 {
		t.Fatalf("before commit = %+v, error=%v", before, err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := service.Reconcile(ctx, account.ID())
	if err != nil || !after.Consistent || after.StoredBalance.String() != "75.00" || after.CheckedEntries != 2 {
		t.Fatalf("after commit = %+v, error=%v", after, err)
	}
}
