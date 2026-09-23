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
)

// Exercise every migration against an isolated schema so this test cannot
// disturb the application's tables or the fixtures used by other tests.
func TestMigrationsUpDownUpInDisposableSchema(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminURL := os.Getenv("APP_DATABASE_ADMIN_URL")
	if adminURL == "" {
		adminURL = "postgres://wager_admin:wager_admin_local@localhost:5432/wagering?sslmode=disable"
	}
	config, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())

	schema := "migration_test_" + strings.ReplaceAll(randomUUID(t), "-", "")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := conn.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop disposable migration schema: %v", err)
		}
	}()
	if _, err := conn.Exec(ctx, "SET search_path TO "+identifier); err != nil {
		t.Fatal(err)
	}

	apply := func(version int, direction string) {
		t.Helper()
		name := fmt.Sprintf("%06d_%s.%s.sql", version, map[int]string{
			1: "initial", 2: "financial_semantics", 3: "pending_reference_deadline", 4: "wallet_ledger_continuity",
		}[version], direction)
		contents, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(contents)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	checkVersions := func(want string) {
		t.Helper()
		var got string
		if err := conn.QueryRow(ctx, `SELECT coalesce(string_agg(version::text, ',' ORDER BY version), '') FROM schema_migrations`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("schema versions = %q, want %q", got, want)
		}
	}
	checkConstraint := func(want bool) {
		t.Helper()
		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_constraint c
			JOIN pg_namespace n ON n.oid = c.connamespace
			WHERE n.nspname = $1 AND c.conname = 'wager_pending_reference_due_before_expiry'
		)`, schema).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("pending reference deadline constraint exists = %t, want %t", exists, want)
		}
	}

	for _, version := range []int{1, 2, 3, 4} {
		apply(version, "up")
	}
	checkVersions("1,2,3,4")
	checkConstraint(true)
	for _, version := range []int{4, 3, 2} {
		apply(version, "down")
		checkVersions(map[int]string{4: "1,2,3", 3: "1,2", 2: "1"}[version])
	}
	checkConstraint(false)
	apply(1, "down")
	var remaining *string
	if err := conn.QueryRow(ctx, `SELECT to_regclass($1)::text`, schema+".schema_migrations").Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != nil {
		t.Fatalf("schema_migrations remains after down: %s", *remaining)
	}
	for _, version := range []int{1, 2, 3, 4} {
		apply(version, "up")
	}
	checkVersions("1,2,3,4")
	checkConstraint(true)
}
