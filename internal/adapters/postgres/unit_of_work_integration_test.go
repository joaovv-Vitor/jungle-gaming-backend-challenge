//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUnitOfWorkIsolationLevels(t *testing.T) {
	databaseURL := os.Getenv("APP_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	unit := NewUnitOfWork(pool)

	if err := unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		assertSetting(t, ctx, tx, "transaction_isolation", "read committed")
		assertSetting(t, ctx, tx, "transaction_read_only", "off")
		return nil
	}); err != nil {
		t.Fatalf("ReadCommitted() error = %v", err)
	}

	if err := unit.RepeatableReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		assertSetting(t, ctx, tx, "transaction_isolation", "repeatable read")
		assertSetting(t, ctx, tx, "transaction_read_only", "on")
		return nil
	}); err != nil {
		t.Fatalf("RepeatableReadOnly() error = %v", err)
	}
}

func TestReadCommittedRetryRollsBackAndRepeatsWholeTransaction(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	unit := NewUnitOfWork(pool)

	for _, state := range []string{"40P01", "40001"} {
		t.Run(state, func(t *testing.T) {
			walletID, playerID := randomUUID(t), randomUUID(t)
			attempts := 0
			err := unit.ReadCommittedWithRetry(ctx, func(ctx context.Context, tx pgx.Tx) error {
				attempts++
				_, err := tx.Exec(ctx, `INSERT INTO wallets(id, player_id, currency, balance_minor, version, created_at, updated_at)
					VALUES ($1, $2, 'BRL', 0, 1, now(), now())`, walletID, playerID)
				if err != nil {
					return err
				}
				if attempts == 1 {
					return &pgconn.PgError{Code: state}
				}
				return nil
			})
			if err != nil || attempts != 2 {
				t.Fatalf("retry error = %v, attempts = %d, want 2", err, attempts)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallets WHERE id=$1`, walletID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("persisted wallets = %d, error = %v, want 1", count, err)
			}
		})
	}
}

func TestReadCommittedRetryRecoversFromRealDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	unit := NewUnitOfWork(pool)
	firstKey := time.Now().UnixNano()
	keys := [2]int64{firstKey, firstKey + 1}
	firstLocks := make(chan struct{}, 2)
	bothReady := make(chan struct{})
	go func() {
		for range 2 {
			select {
			case <-firstLocks:
			case <-ctx.Done():
				return
			}
		}
		close(bothReady)
	}()
	type outcome struct {
		attempts int
		err      error
	}
	results := make(chan outcome, 2)
	for worker := range 2 {
		go func(worker int) {
			attempts := 0
			err := unit.ReadCommittedWithRetry(ctx, func(ctx context.Context, tx pgx.Tx) error {
				attempts++
				if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, keys[worker]); err != nil {
					return err
				}
				if attempts == 1 {
					firstLocks <- struct{}{}
					select {
					case <-bothReady:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, keys[1-worker])
				return err
			})
			results <- outcome{attempts: attempts, err: err}
		}(worker)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.attempts+second.attempts != 3 {
		t.Fatalf("deadlock outcomes = %+v and %+v, want one retry and both committed", first, second)
	}
}

func assertSetting(t *testing.T, ctx context.Context, tx pgx.Tx, name, want string) {
	t.Helper()
	var got string
	if err := tx.QueryRow(ctx, "SHOW "+name).Scan(&got); err != nil {
		t.Fatalf("SHOW %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("SHOW %s = %q, want %q", name, got, want)
	}
}
