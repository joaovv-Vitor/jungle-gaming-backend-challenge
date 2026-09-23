//go:build integration

package bootstrap_test

import (
	"context"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPendingReferencesResolveAndExpireAfterFullProcessRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	bin := filepath.Join(t.TempDir(), "wager-service")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	port := freePort(t)
	process := startServer(t, ctx, bin, port, 0, "APP_REFERENCE_POLL_INTERVAL=1h")
	t.Cleanup(func() {
		if process != nil {
			process.stop(t)
		}
	})
	process.waitReady(t, ctx)
	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	internalToken := clientToken(t, ctx, issuer, "internal-service", "internal-service-local")
	providerToken := clientToken(t, ctx, issuer, "provider-a", "provider-a-local")
	pool, err := pgxpool.New(ctx, envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	type fixture struct {
		walletID, playerID, transactionID, externalID, referenceID string
		payload                                                    map[string]any
	}
	newPending := func(name string) fixture {
		playerID := newUUID(t)
		opened := requestJSON(t, ctx, port, http.MethodPost, "/wallets", internalToken, "", map[string]any{
			"playerId": playerID, "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
		})
		if opened.status != http.StatusCreated {
			t.Fatalf("open %s: %d %s", name, opened.status, opened.body)
		}
		walletID := opened.stringField(t, "id")
		externalID := name + "-refund-" + walletID
		referenceID := name + "-bet-" + walletID
		payload := map[string]any{
			"providerId": "provider-a", "externalTransactionId": externalID,
			"playerId": playerID, "walletId": walletID, "roundId": "restart-round",
			"gameId": "restart-game", "kind": "REFUND", "referenceExternalTransactionId": referenceID,
			"money": map[string]string{"amount": "25.00", "currency": "BRL"},
		}
		pending := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, externalID, payload)
		if pending.status != http.StatusAccepted || pending.field("status") != "PENDING_REFERENCE" {
			t.Fatalf("pending %s: %d %s", name, pending.status, pending.body)
		}
		return fixture{walletID, playerID, pending.stringField(t, "transactionId"), externalID, referenceID, payload}
	}
	resolved := newPending("resolved")
	expired := newPending("expired")
	process.stop(t)
	process = nil
	for _, item := range []fixture{resolved, expired} {
		var status string
		var scheduled, hasExpiry bool
		if err := pool.QueryRow(ctx, `SELECT status, next_attempt_at IS NOT NULL, expires_at IS NOT NULL FROM wager_transactions WHERE id=$1`, item.transactionID).Scan(&status, &scheduled, &hasExpiry); err != nil {
			t.Fatal(err)
		}
		if status != "PENDING_REFERENCE" || !scheduled || !hasExpiry {
			t.Fatalf("pending did not survive shutdown: %s scheduled=%v expiry=%v", status, scheduled, hasExpiry)
		}
	}
	// Expire one fixture while no application process is running. The other
	// receives its reference only after the replacement process is ready.
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET expires_at=clock_timestamp()-interval '1 second', next_attempt_at='2000-01-01' WHERE id=$1`, expired.transactionID); err != nil {
		t.Fatal(err)
	}
	process = startServer(t, ctx, bin, port, 1, "APP_REFERENCE_POLL_INTERVAL=100ms")
	process.waitReady(t, ctx)
	bet := map[string]any{
		"providerId": "provider-a", "externalTransactionId": resolved.referenceID,
		"playerId": resolved.playerID, "walletId": resolved.walletID, "roundId": "restart-round",
		"gameId": "restart-game", "kind": "BET",
		"money": map[string]string{"amount": "25.00", "currency": "BRL"},
	}
	reference := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, resolved.referenceID, bet)
	if reference.status != http.StatusOK || reference.field("status") != "PROCESSED" {
		t.Fatalf("reference after restart: %d %s", reference.status, reference.body)
	}
	if _, err := pool.Exec(ctx, `UPDATE wager_transactions SET next_attempt_at='2000-01-01' WHERE id=$1 AND status='PENDING_REFERENCE'`, resolved.transactionID); err != nil {
		t.Fatal(err)
	}
	resolvedResult := waitForTransactionStatus(t, ctx, port, providerToken, resolved.transactionID, "PROCESSED")
	if resolvedResult.field("failureCode") != "" {
		t.Fatalf("resolved reference has failure: %s", resolvedResult.body)
	}
	expiredResult := waitForTransactionStatus(t, ctx, port, providerToken, expired.transactionID, "REJECTED")
	if expiredResult.field("failureCode") != "REFERENCE_NOT_FOUND" {
		t.Fatalf("expired reference failure: %s", expiredResult.body)
	}
	assertWalletState(t, ctx, resolved.walletID, 10_000, 3, 3, 7)
	assertWalletState(t, ctx, expired.walletID, 10_000, 1, 1, 4)
	assertPendingEvents(t, ctx, pool, resolved.transactionID, []string{
		"WagerTransactionPendingReference", "WagerTransactionProcessed",
	})
	assertPendingEvents(t, ctx, pool, expired.transactionID, []string{
		"WagerTransactionPendingReference", "WagerTransactionRejected",
	})
	for _, item := range []struct {
		fixture fixture
		status  string
	}{
		{resolved, "PROCESSED"}, {expired, "REJECTED"},
	} {
		replay := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, item.fixture.externalID, item.fixture.payload)
		if replay.status != http.StatusOK || replay.field("status") != item.status || replay.data["idempotentReplay"] != true {
			t.Fatalf("terminal replay after restart: %d %s", replay.status, replay.body)
		}
	}
	process.stop(t)
	process = nil
}

func waitForTransactionStatus(t *testing.T, ctx context.Context, port int, token, transactionID, want string) apiResponse {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	for {
		response, err := sendJSON(ctx, port, http.MethodGet, "/wagering/transactions/"+transactionID, token, "", nil)
		if err == nil && response.status == http.StatusOK && response.field("status") == want {
			return response
		}
		select {
		case <-deadline.C:
			t.Fatalf("transaction %s did not reach %s: status=%d error=%v body=%s", transactionID, want, response.status, err, response.body)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func assertPendingEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, transactionID string, want []string) {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT event_type FROM outbox_events WHERE aggregate_id=$1`, transactionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		got = append(got, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("events for %s = %v, want %v", transactionID, got, want)
	}
	sort.Strings(got)
	sort.Strings(want)
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("events for %s = %v, want %v", transactionID, got, want)
		}
	}
}
