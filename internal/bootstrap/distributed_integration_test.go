//go:build integration

package bootstrap_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This test deliberately starts OS processes, not three services in one test process.
func TestThreeProcessesSerializeAndReplayAfterRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)
	root := filepath.Clean(filepath.Join("..", ".."))
	bin := filepath.Join(t.TempDir(), "wager-service")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}

	ports := make([]int, 3)
	usedPorts := make(map[int]bool, len(ports))
	for i := range ports {
		for {
			port := freePort(t)
			if !usedPorts[port] {
				ports[i] = port
				usedPorts[port] = true
				break
			}
		}
	}
	processes := make([]*serverProcess, 3)
	startAll := func() {
		for i := range processes {
			processes[i] = startServer(t, ctx, bin, ports[i], i)
		}
	}
	stopAll := func() {
		for i, process := range processes {
			if process != nil {
				process.stop(t)
				processes[i] = nil
			}
		}
	}
	t.Cleanup(stopAll)
	startAll()
	for _, process := range processes {
		process.waitReady(t, ctx)
	}

	issuer := envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering")
	internalToken := clientToken(t, ctx, issuer, "internal-service", "internal-service-local")
	providerToken := clientToken(t, ctx, issuer, "provider-a", "provider-a-local")
	playerID := newUUID(t)
	open := requestJSON(t, ctx, ports[0], http.MethodPost, "/wallets", internalToken, "", map[string]any{
		"playerId": playerID, "initialBalance": map[string]string{"amount": "100.00", "currency": "BRL"},
	})
	if open.status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", open.status, open.body)
	}
	walletID := open.stringField(t, "id")

	// Each request reaches a distinct process and pool after the same barrier opens.
	bet := func(externalID, amount string) map[string]any {
		return map[string]any{
			"providerId": "provider-a", "externalTransactionId": externalID,
			"playerId": playerID, "walletId": walletID, "roundId": "round-1",
			"gameId": "game-1", "kind": "BET",
			"money": map[string]string{"amount": amount, "currency": "BRL"},
		}
	}
	start := make(chan struct{})
	type outcome struct {
		response apiResponse
		err      error
	}
	results := make([]outcome, 2)
	var group sync.WaitGroup
	for i := range results {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			results[i].response, results[i].err = sendJSON(ctx, ports[i], http.MethodPost, "/wagering/transactions", providerToken,
				"bet-"+fmt.Sprint(i)+"-"+walletID, bet("bet-"+fmt.Sprint(i)+"-"+walletID, "80.00"))
		}(i)
	}
	close(start)
	group.Wait()
	processed, rejected := 0, 0
	for i, result := range results {
		if result.err != nil || result.response.status != http.StatusOK {
			t.Fatalf("bet %d: status=%d err=%v body=%s", i, result.response.status, result.err, result.response.body)
		}
		switch result.response.field("status") {
		case "PROCESSED":
			processed++
		case "REJECTED":
			rejected++
			if result.response.field("failureCode") != "BET_INSUFFICIENT_FUNDS" {
				t.Fatalf("bet %d rejection: %s", i, result.response.body)
			}
		default:
			t.Fatalf("bet %d result: %s", i, result.response.body)
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed/rejected = %d/%d, want 1/1", processed, rejected)
	}

	// Fifty identical deliveries are distributed across all three real processes.
	duplicateID := "duplicate-" + walletID
	duplicate := bet(duplicateID, "5.00")
	start = make(chan struct{})
	duplicates := make([]outcome, 50)
	for i := range duplicates {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			<-start
			duplicates[i].response, duplicates[i].err = sendJSON(ctx, ports[i%3], http.MethodPost, "/wagering/transactions", providerToken, duplicateID, duplicate)
		}(i)
	}
	close(start)
	group.Wait()
	first := 0
	transactionID := ""
	for i, result := range duplicates {
		if result.err != nil || result.response.status != http.StatusOK || result.response.field("status") != "PROCESSED" {
			t.Fatalf("duplicate %d: status=%d err=%v body=%s", i, result.response.status, result.err, result.response.body)
		}
		if transactionID == "" {
			transactionID = result.response.field("transactionId")
		}
		if result.response.field("transactionId") != transactionID {
			t.Fatalf("duplicate %d has different transaction: %s", i, result.response.body)
		}
		if result.response.data["idempotentReplay"] == false {
			first++
		}
	}
	if first != 1 {
		t.Fatalf("first deliveries = %d, want 1", first)
	}
	assertWalletState(t, ctx, walletID, 1500, 3, 3, 7)
	proveIndependentWalletProgress(t, ctx, ports, internalToken, providerToken, walletID, playerID)
	assertWalletState(t, ctx, walletID, 1400, 4, 4, 9)

	stopAll()
	startAll()
	for _, process := range processes {
		process.waitReady(t, ctx)
	}
	replay := requestJSON(t, ctx, ports[2], http.MethodPost, "/wagering/transactions", providerToken, duplicateID, duplicate)
	if replay.status != http.StatusOK || replay.field("transactionId") != transactionID || replay.data["idempotentReplay"] != true {
		t.Fatalf("replay after full restart: %d %s", replay.status, replay.body)
	}
	assertWalletState(t, ctx, walletID, 1400, 4, 4, 9)
	stopAll()
}

func proveIndependentWalletProgress(t *testing.T, ctx context.Context, ports []int, internalToken, providerToken, lockedWalletID, lockedPlayerID string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	secondPlayerID := newUUID(t)
	open := requestJSON(t, ctx, ports[2], http.MethodPost, "/wallets", internalToken, "", map[string]any{
		"playerId": secondPlayerID, "initialBalance": map[string]string{"amount": "10.00", "currency": "BRL"},
	})
	if open.status != http.StatusCreated {
		t.Fatalf("open independent wallet: %d %s", open.status, open.body)
	}
	secondWalletID := open.stringField(t, "id")
	locked, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(ctx)
	var id string
	if err := locked.QueryRow(ctx, `SELECT id::text FROM wallets WHERE id=$1 FOR NO KEY UPDATE`, lockedWalletID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	firstID := "locked-" + lockedWalletID
	request := func(walletID, playerID, externalID string) map[string]any {
		return map[string]any{
			"providerId": "provider-a", "externalTransactionId": externalID,
			"playerId": playerID, "walletId": walletID, "roundId": "round-independent",
			"gameId": "game-1", "kind": "BET",
			"money": map[string]string{"amount": "1.00", "currency": "BRL"},
		}
	}
	type result struct {
		response apiResponse
		err      error
	}
	blocked := make(chan result, 1)
	go func() {
		response, err := sendJSON(ctx, ports[0], http.MethodPost, "/wagering/transactions", providerToken, firstID,
			request(lockedWalletID, lockedPlayerID, firstID))
		blocked <- result{response, err}
	}()
	// Observe the real PostgreSQL row-lock wait; a timing delay would not prove it.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FOR NO KEY UPDATE%'`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case outcome := <-blocked:
			t.Fatalf("locked wallet request finished early: status=%d err=%v body=%s", outcome.response.status, outcome.err, outcome.response.body)
		case <-deadline.C:
			t.Fatal("did not observe wallet row-lock wait")
		case <-time.After(20 * time.Millisecond):
		}
	}
	secondID := "independent-" + secondWalletID
	other := requestJSON(t, ctx, ports[1], http.MethodPost, "/wagering/transactions", providerToken, secondID,
		request(secondWalletID, secondPlayerID, secondID))
	if other.status != http.StatusOK || other.field("status") != "PROCESSED" {
		t.Fatalf("independent wallet did not progress: %d %s", other.status, other.body)
	}
	select {
	case outcome := <-blocked:
		t.Fatalf("locked wallet request finished before lock release: status=%d err=%v", outcome.response.status, outcome.err)
	default:
	}
	if err := locked.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-blocked:
		if outcome.err != nil || outcome.response.status != http.StatusOK || outcome.response.field("status") != "PROCESSED" {
			t.Fatalf("released wallet request: status=%d err=%v body=%s", outcome.response.status, outcome.err, outcome.response.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("locked wallet request did not complete after release")
	}
	assertWalletState(t, ctx, secondWalletID, 900, 2, 2, 4)
}

type serverProcess struct {
	cmd  *exec.Cmd
	log  *os.File
	port int
}

func startServer(t *testing.T, ctx context.Context, bin string, port, index int, overrides ...string) *serverProcess {
	t.Helper()
	log, err := os.Create(filepath.Join(filepath.Dir(bin), fmt.Sprintf("server-%d-%d.log", index, port)))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("APP_HTTP_ADDR=127.0.0.1:%d", port),
		"APP_DATABASE_URL="+envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"),
		"APP_OIDC_ISSUER="+envOr("APP_OIDC_ISSUER", "http://localhost:8081/realms/wagering"),
		"APP_OIDC_JWKS_URL="+envOr("APP_OIDC_JWKS_URL", "http://localhost:8081/realms/wagering/protocol/openid-connect/certs"),
		"APP_SQS_ENDPOINT="+envOr("APP_SQS_ENDPOINT", "http://localhost:4566"),
		"APP_DATABASE_MIN_CONNS=1", "APP_REFERENCE_WORKERS=1", "APP_OUTBOX_WORKERS=1",
		"APP_SQS_LONG_POLL=1s", "APP_SQS_SHUTDOWN_TIMEOUT=5s")
	cmd.Env = append(cmd.Env, overrides...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		t.Fatal(err)
	}
	t.Logf("instance %d: PID=%d port=%d", index, cmd.Process.Pid, port)
	return &serverProcess{cmd: cmd, log: log, port: port}
}

func (p *serverProcess) waitReady(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health/ready", p.port), nil)
		response, err := (&http.Client{Timeout: time.Second}).Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("instance PID=%d port=%d did not become ready (last error: %v)", p.cmd.Process.Pid, p.port, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *serverProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Errorf("signal PID %d: %v", p.cmd.Process.Pid, err)
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("PID %d shutdown: %v", p.cmd.Process.Pid, err)
		}
	case <-time.After(12 * time.Second):
		p.cmd.Process.Kill()
		<-done
		t.Errorf("PID %d did not gracefully stop", p.cmd.Process.Pid)
	}
	p.log.Close()
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func clientToken(t *testing.T, ctx context.Context, issuer, clientID, secret string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.AccessToken == "" {
		t.Fatalf("token for %s: %s", clientID, response.Status)
	}
	return body.AccessToken
}

type apiResponse struct {
	status int
	body   string
	data   map[string]any
}

func (r apiResponse) field(name string) string {
	value, _ := r.data[name].(string)
	return value
}

func (r apiResponse) stringField(t *testing.T, name string) string {
	t.Helper()
	value := r.field(name)
	if value == "" {
		t.Fatalf("missing %s in %s", name, r.body)
	}
	return value
}

func requestJSON(t *testing.T, ctx context.Context, port int, method, path, token, key string, payload any) apiResponse {
	t.Helper()
	response, err := sendJSON(ctx, port, method, path, token, key, payload)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func sendJSON(ctx context.Context, port int, method, path, token, key string, payload any) (apiResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return apiResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), bytes.NewReader(body))
	if err != nil {
		return apiResponse{}, err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-ID", "distributed-integration")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return apiResponse{}, err
	}
	defer response.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
		return apiResponse{}, err
	}
	encoded, _ := json.Marshal(data)
	return apiResponse{status: response.StatusCode, body: string(encoded), data: data}, nil
}

func assertWalletState(t *testing.T, ctx context.Context, walletID string, balance, version, ledger, events int64) {
	t.Helper()
	pool, err := pgxpool.New(ctx, envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var gotBalance, gotVersion, gotLedger, gotEvents, ledgerSum int64
	err = pool.QueryRow(ctx, `SELECT balance_minor, version FROM wallets WHERE id=$1`, walletID).Scan(&gotBalance, &gotVersion)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(CASE WHEN direction='CREDIT' THEN amount_minor ELSE -amount_minor END)::bigint,0) FROM wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&gotLedger, &ledgerSum)
	if err != nil {
		t.Fatal(err)
	}
	err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 OR aggregate_id IN (SELECT id FROM wager_transactions WHERE wallet_id=$1)`, walletID).Scan(&gotEvents)
	if err != nil {
		t.Fatal(err)
	}
	if gotBalance != balance || gotVersion != version || gotLedger != ledger || gotEvents != events || ledgerSum != balance {
		t.Fatalf("wallet state: balance=%d version=%d ledger=%d events=%d ledgerSum=%d; want %d/%d/%d/%d", gotBalance, gotVersion, gotLedger, gotEvents, ledgerSum, balance, version, ledger, events)
	}
}

func newUUID(t *testing.T) string {
	t.Helper()
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
