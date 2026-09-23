//go:build integration

package bootstrap_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLedgerCursorRemainsStableWhileNewMovementsCommit(t *testing.T) {
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
	playerID := newUUID(t)
	opened := requestJSON(t, ctx, port, http.MethodPost, "/wallets", internalToken, "", map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	if opened.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", opened.status, opened.body)
	}
	walletID := opened.stringField(t, "id")
	bet := func(index int, amount string) (string, map[string]any) {
		externalID := fmt.Sprintf("ledger-page-%d-%s", index, walletID)
		return externalID, map[string]any{
			"providerId": "provider-a", "externalTransactionId": externalID,
			"playerId": playerID, "walletId": walletID, "roundId": "pagination-round",
			"gameId": "pagination-game", "kind": "BET",
			"money": map[string]string{"amount": amount, "currency": "BRL"},
		}
	}
	submitBet := func(index int, amount string) {
		externalID, payload := bet(index, amount)
		response := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, externalID, payload)
		if response.status != http.StatusOK || response.field("status") != "PROCESSED" {
			t.Fatalf("bet %d: %d %s", index, response.status, response.body)
		}
	}
	for index := 1; index <= 4; index++ {
		submitBet(index, "10.00")
	}
	ledgerPath := "/wallets/" + walletID + "/ledger?limit=2"
	first := requestJSON(t, ctx, port, http.MethodGet, ledgerPath, internalToken, "", nil)
	if first.status != http.StatusOK {
		t.Fatalf("first ledger page: %d %s", first.status, first.body)
	}
	firstCursor := first.stringField(t, "nextCursor")
	decoded, err := base64.RawURLEncoding.DecodeString(firstCursor)
	if err != nil {
		t.Fatal(err)
	}
	var cursor struct {
		Version  int    `json:"version"`
		WalletID string `json:"walletId"`
	}
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Version != 1 || cursor.WalletID != walletID {
		t.Fatalf("cursor format = %+v, error=%v", cursor, err)
	}
	var cursorFields map[string]any
	if err := json.Unmarshal(decoded, &cursorFields); err != nil {
		t.Fatal(err)
	}
	cursorFields["version"] = float64(2)
	wrongVersionBytes, err := json.Marshal(cursorFields)
	if err != nil {
		t.Fatal(err)
	}
	wrongVersionCursor := base64.RawURLEncoding.EncodeToString(wrongVersionBytes)
	other := requestJSON(t, ctx, port, http.MethodPost, "/wallets", internalToken, "", map[string]any{
		"playerId": newUUID(t), "initialBalance": map[string]string{"amount": "0.00", "currency": "BRL"},
	})
	if other.status != http.StatusCreated {
		t.Fatalf("other wallet: %d %s", other.status, other.body)
	}
	for _, scenario := range []struct{ name, path string }{
		{"wrong wallet", "/wallets/" + other.stringField(t, "id") + "/ledger?limit=2&cursor=" + firstCursor},
		{"unsupported cursor version", ledgerPath + "&cursor=" + wrongVersionCursor},
		{"invalid encoding", ledgerPath + "&cursor=not-a-valid-cursor"},
		{"invalid limit", "/wallets/" + walletID + "/ledger?limit=101"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := requestJSON(t, ctx, port, http.MethodGet, scenario.path, internalToken, "", nil)
			if response.status != http.StatusBadRequest || response.field("code") != "INVALID_PAGINATION" {
				t.Fatalf("invalid page request: %d %s", response.status, response.body)
			}
		})
	}
	// A committed movement and another write racing with later page requests
	// must not alter the set already bounded by the first page's keyset cursor.
	submitBet(5, "5.00")
	var group sync.WaitGroup
	var concurrent apiResponse
	var concurrentErr error
	group.Add(1)
	go func() {
		defer group.Done()
		externalID, payload := bet(6, "5.00")
		concurrent, concurrentErr = sendJSON(ctx, port, http.MethodPost, "/wagering/transactions", providerToken, externalID, payload)
	}()
	seen := make(map[string]bool)
	versions := make([]int, 0, 5)
	consume := func(response apiResponse) {
		entries, ok := response.data["entries"].([]any)
		if !ok || len(entries) == 0 || len(entries) > 2 {
			t.Fatalf("invalid ledger entries: %s", response.body)
		}
		for _, raw := range entries {
			entry, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("invalid ledger entry: %v", raw)
			}
			id, _ := entry["id"].(string)
			version, _ := entry["walletVersion"].(float64)
			if id == "" || seen[id] || version < 1 || version > 5 {
				t.Fatalf("duplicated or out-of-snapshot entry: %v", entry)
			}
			seen[id] = true
			versions = append(versions, int(version))
		}
	}
	consume(first)
	nextCursor := firstCursor
	for nextCursor != "" {
		page := requestJSON(t, ctx, port, http.MethodGet, ledgerPath+"&cursor="+nextCursor, internalToken, "", nil)
		if page.status != http.StatusOK {
			t.Fatalf("ledger page: %d %s", page.status, page.body)
		}
		consume(page)
		nextCursor = page.field("nextCursor")
	}
	group.Wait()
	if concurrentErr != nil || concurrent.status != http.StatusOK || concurrent.field("status") != "PROCESSED" {
		t.Fatalf("concurrent movement: status=%d error=%v body=%s", concurrent.status, concurrentErr, concurrent.body)
	}
	if len(versions) != 5 {
		t.Fatalf("ledger traversal = %v, want five original entries", versions)
	}
	for index, version := range versions {
		if version != 5-index {
			t.Fatalf("ledger order = %v, want [5 4 3 2 1]", versions)
		}
	}
	assertWalletState(t, ctx, walletID, 5_000, 7, 7, 14)
	process.stop(t)
	process = nil
}
