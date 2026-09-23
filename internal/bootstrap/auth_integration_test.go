//go:build integration

package bootstrap_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRealKeycloakAuthorizationHasNoUnauthorizedFinancialEffects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	bin := filepath.Join(t.TempDir(), "wager-service")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	port := freePort(t)
	process := startServer(t, ctx, bin, port, 0)
	t.Cleanup(func() {
		if process != nil {
			process.stop(t)
		}
	})
	process.waitReady(t, ctx)

	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	internalToken := clientToken(t, ctx, issuer, "internal-service", "internal-service-local")
	providerToken := clientToken(t, ctx, issuer, "provider-a", "provider-a-local")
	otherProviderToken := clientToken(t, ctx, issuer, "provider-b", "provider-b-local")
	playerID := newUUID(t)
	opened := requestJSON(t, ctx, port, http.MethodPost, "/wallets", internalToken, "", map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	if opened.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", opened.status, opened.body)
	}
	walletID := opened.stringField(t, "id")
	externalID := "authorized-" + walletID
	secretSentinel := "sensitive-auth-" + walletID
	bet := map[string]any{
		"providerId": "provider-a", "externalTransactionId": externalID,
		"playerId": playerID, "walletId": walletID, "roundId": "auth-round",
		"gameId": secretSentinel, "kind": "BET",
		"money": map[string]string{"amount": "20.00", "currency": "BRL"},
	}
	created := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, secretSentinel, bet)
	if created.status != http.StatusOK || created.field("status") != "PROCESSED" {
		t.Fatalf("authorized bet: %d %s", created.status, created.body)
	}
	transactionID := created.stringField(t, "transactionId")
	assertWalletState(t, ctx, walletID, 8000, 2, 2, 4)
	unauthorizedPlayerID := newUUID(t)
	newBet := func(externalID string) map[string]any {
		return map[string]any{
			"providerId": "provider-a", "externalTransactionId": externalID,
			"playerId": playerID, "walletId": walletID, "roundId": "auth-round",
			"gameId": "auth-game", "kind": "BET",
			"money": map[string]string{"amount": "5.00", "currency": "BRL"},
		}
	}

	for _, scenario := range []struct {
		name, method, path, token, key string
		payload                        any
		wantStatus                     int
	}{
		{"no token wallet read", http.MethodGet, "/wallets/" + walletID, "", "", nil, http.StatusUnauthorized},
		{"provider wallet read", http.MethodGet, "/wallets/" + walletID, providerToken, "", nil, http.StatusForbidden},
		{"other provider wallet read", http.MethodGet, "/wallets/" + walletID, otherProviderToken, "", nil, http.StatusForbidden},
		{"provider ledger read", http.MethodGet, "/wallets/" + walletID + "/ledger", providerToken, "", nil, http.StatusForbidden},
		{"provider reconciliation", http.MethodPost, "/wallets/" + walletID + "/reconciliation", providerToken, "", nil, http.StatusForbidden},
		{"provider metrics", http.MethodGet, "/metrics", providerToken, "", nil, http.StatusForbidden},
		{"provider wallet open", http.MethodPost, "/wallets", providerToken, "", map[string]any{
			"playerId": unauthorizedPlayerID, "initialBalance": map[string]string{"amount": "50.00", "currency": "BRL"},
		}, http.StatusForbidden},
		{"internal wager submit", http.MethodPost, "/wagering/transactions", internalToken, "unauthorized-" + walletID, newBet("unauthorized-" + walletID), http.StatusForbidden},
		{"internal transaction read", http.MethodGet, "/wagering/transactions/" + transactionID, internalToken, "", nil, http.StatusForbidden},
		{"tampered provider token", http.MethodPost, "/wagering/transactions", providerToken + "tampered", "tampered-" + walletID, newBet("tampered-" + walletID), http.StatusUnauthorized},
		{"body provider mismatch", http.MethodPost, "/wagering/transactions", providerToken, "mismatch-" + walletID, map[string]any{
			"providerId": "provider-b", "externalTransactionId": "mismatch-" + walletID,
			"playerId": playerID, "walletId": walletID, "roundId": "auth-round",
			"gameId": "auth-game", "kind": "BET",
			"money": map[string]string{"amount": "20.00", "currency": "BRL"},
		}, http.StatusForbidden},
		{"other provider replays A", http.MethodPost, "/wagering/transactions", otherProviderToken, externalID, bet, http.StatusForbidden},
		{"other provider reads A transaction", http.MethodGet, "/wagering/transactions/" + transactionID, otherProviderToken, "", nil, http.StatusNotFound},
		{"other provider reads A external ID", http.MethodGet, "/providers/provider-b/wagering/transactions/" + externalID, otherProviderToken, "", nil, http.StatusNotFound},
		{"path provider mismatch", http.MethodGet, "/providers/provider-b/wagering/transactions/" + externalID, providerToken, "", nil, http.StatusForbidden},
		{"no token transaction read", http.MethodGet, "/wagering/transactions/" + transactionID, "", "", nil, http.StatusUnauthorized},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := requestJSON(t, ctx, port, scenario.method, scenario.path, scenario.token, scenario.key, scenario.payload)
			if response.status != scenario.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.status, response.body, scenario.wantStatus)
			}
			if response.field("transactionId") != "" || response.field("id") != "" {
				t.Fatalf("unauthorized response exposed resource: %s", response.body)
			}
		})
	}
	assertWalletState(t, ctx, walletID, 8000, 2, 2, 4)
	pool, err := pgxpool.New(ctx, envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var unauthorizedWallets int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallets WHERE player_id=$1`, unauthorizedPlayerID).Scan(&unauthorizedWallets); err != nil || unauthorizedWallets != 0 {
		t.Fatalf("unauthorized wallet creations = %d, error=%v", unauthorizedWallets, err)
	}
	visible := requestJSON(t, ctx, port, http.MethodGet, fmt.Sprintf("/wagering/transactions/%s", transactionID), providerToken, "", nil)
	if visible.status != http.StatusOK || visible.field("transactionId") != transactionID {
		t.Fatalf("authorized transaction read: %d %s", visible.status, visible.body)
	}
	logPath := process.log.Name()
	process.stop(t)
	process = nil
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), "wager submission handled") {
		t.Fatal("expected financial operation log is missing")
	}
	for _, secret := range []string{internalToken, providerToken, otherProviderToken,
		secretSentinel, "internal-service-local", "provider-a-local", "provider-b-local"} {
		if strings.Contains(string(logs), secret) {
			t.Fatalf("process log exposed a credential or financial payload sentinel")
		}
	}
}
