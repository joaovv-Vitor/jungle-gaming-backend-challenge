//go:build integration

package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
)

// Scheduler tests need an empty database namespace because Claim intentionally
// searches all due work rather than a test-specific correlation ID.
func isolatedSchedulerPools(t *testing.T, ctx context.Context) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	adminURL := os.Getenv("APP_DATABASE_ADMIN_URL")
	if adminURL == "" {
		adminURL = "postgres://wager_admin:wager_admin_local@localhost:5432/wagering?sslmode=disable"
	}
	adminConfig, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	adminConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	schema := "scheduler_test_" + strings.ReplaceAll(randomUUID(t), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop scheduler test schema: %v", err)
		}
		if err := admin.Close(cleanupCtx); err != nil {
			t.Errorf("close scheduler schema connection: %v", err)
		}
	})
	if _, err := admin.Exec(ctx, "SET search_path TO "+identifier); err != nil {
		t.Fatal(err)
	}
	for version, name := range []string{"initial", "financial_semantics", "pending_reference_deadline", "wallet_ledger_continuity"} {
		path := filepath.Join("..", "..", "..", "migrations", fmt.Sprintf("%06d_%s.up.sql", version+1, name))
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s in scheduler schema: %v", path, err)
		}
	}
	newPool := func() *pgxpool.Pool {
		t.Helper()
		config, err := pgxpool.ParseConfig(adminURL)
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["search_path"] = schema
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	return newPool(), newPool()
}

func financialServicesOnPool(pool *pgxpool.Pool) (*applicationwallet.Service, *applicationwagering.Service) {
	unit := NewUnitOfWork(pool)
	wallets := NewWalletRepository()
	wagers := NewWagerRepository()
	entries := NewLedgerRepository()
	outbox := NewOutboxRepository()
	return applicationwallet.NewService(NewWalletStore(unit, wallets, wagers, entries, outbox)),
		applicationwagering.NewService(NewWagerStore(unit, wallets, wagers, entries, outbox))
}
