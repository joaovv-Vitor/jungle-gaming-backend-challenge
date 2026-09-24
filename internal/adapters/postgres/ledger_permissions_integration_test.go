//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
)

func TestRuntimeRoleCannotMutateLedger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, _ := financialServices(t, ctx)
	defer pool.Close()
	account, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 10_000, "BRL"),
		CorrelationID: "ledger-permissions-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name  string
		query string
	}{
		{"UPDATE", `UPDATE wallet_ledger_entries SET amount_minor=amount_minor+1 WHERE wallet_id=$1`},
		{"DELETE", `DELETE FROM wallet_ledger_entries WHERE wallet_id=$1`},
		{"TRUNCATE", `TRUNCATE wallet_ledger_entries`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Even if a grant regresses, rollback keeps the shared test database intact.
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout='2s'`); err != nil {
				t.Fatal(err)
			}
			var args []any
			if scenario.name != "TRUNCATE" {
				args = append(args, account.ID())
			}
			_, err = tx.Exec(ctx, scenario.query, args...)
			var databaseError *pgconn.PgError
			if !errors.As(err, &databaseError) || databaseError.Code != "42501" {
				t.Fatalf("runtime ledger %s error = %v, want insufficient_privilege", scenario.name, err)
			}
		})
	}
	var entries int
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(amount_minor), 0) FROM wallet_ledger_entries WHERE wallet_id=$1`, account.ID()).Scan(&entries, &amount); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || amount != 10_000 {
		t.Fatalf("ledger changed: entries=%d amount=%d", entries, amount)
	}
}
