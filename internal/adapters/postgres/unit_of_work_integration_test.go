//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
