//go:build integration

package bootstrap_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Local fault proxies isolate this test from the Compose app and avoid stopping
// shared PostgreSQL/LocalStack containers or destroying their state.
func TestTemporaryPostgresAndSQSOutagesRecover(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	bin := filepath.Join(t.TempDir(), "wager-service")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/server")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}

	databaseURL := envOr("APP_DATABASE_URL", "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable")
	parsedDatabaseURL, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	postgresProxy := newSwitchTCPProxy(t, parsedDatabaseURL.Host)
	parsedDatabaseURL.Host = postgresProxy.address()
	sqsProxy := newSwitchSQSProxy(t, envOr("APP_SQS_ENDPOINT", "http://localhost:4566"))
	port := freePort(t)
	process := startServer(t, ctx, bin, port, 0,
		"APP_DATABASE_URL="+parsedDatabaseURL.String(), "APP_SQS_ENDPOINT="+sqsProxy.URL)
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
	bet := func(externalID, amount string) map[string]any {
		return map[string]any{
			"providerId": "provider-a", "externalTransactionId": externalID,
			"playerId": playerID, "walletId": walletID, "roundId": "outage-round",
			"gameId": "outage-game", "kind": "BET",
			"money": map[string]string{"amount": amount, "currency": "BRL"},
		}
	}

	postgresProxy.setAvailable(false)
	waitHealthStatus(t, ctx, port, "/health/ready", http.StatusServiceUnavailable)
	waitHealthStatus(t, ctx, port, "/health/live", http.StatusOK)
	firstID := "pg-outage-" + walletID
	firstBet := bet(firstID, "25.00")
	unavailable := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, firstID, firstBet)
	if unavailable.status != http.StatusServiceUnavailable || unavailable.field("code") != "TRANSIENT_FAILURE" {
		t.Fatalf("postgres outage: %d %s", unavailable.status, unavailable.body)
	}
	postgresProxy.setAvailable(true)
	waitHealthStatus(t, ctx, port, "/health/ready", http.StatusOK)
	recovered := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, firstID, firstBet)
	if recovered.status != http.StatusOK || recovered.field("status") != "PROCESSED" {
		t.Fatalf("postgres recovery: %d %s", recovered.status, recovered.body)
	}
	assertWalletState(t, ctx, walletID, 7500, 2, 2, 4)

	sqsProxy.available.Store(false)
	waitHealthStatus(t, ctx, port, "/health/ready", http.StatusServiceUnavailable)
	waitHealthStatus(t, ctx, port, "/health/live", http.StatusOK)
	secondID := "sqs-outage-" + walletID
	second := requestJSON(t, ctx, port, http.MethodPost, "/wagering/transactions", providerToken, secondID, bet(secondID, "5.00"))
	if second.status != http.StatusOK || second.field("status") != "PROCESSED" {
		t.Fatalf("financial commit during SQS outage: %d %s", second.status, second.body)
	}
	if pending := unpublishedEvents(t, ctx, databaseURL, walletID); pending < 2 {
		t.Fatalf("unpublished events during SQS outage = %d, want at least 2", pending)
	}
	sqsProxy.available.Store(true)
	waitHealthStatus(t, ctx, port, "/health/ready", http.StatusOK)
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for pending := unpublishedEvents(t, ctx, databaseURL, walletID); pending != 0; pending = unpublishedEvents(t, ctx, databaseURL, walletID) {
		select {
		case <-deadline.C:
			t.Fatalf("outbox did not recover: %d events remain unpublished", pending)
		case <-time.After(100 * time.Millisecond):
		}
	}
	assertWalletState(t, ctx, walletID, 7000, 3, 3, 6)
	process.stop(t)
	process = nil
}

func waitHealthStatus(t *testing.T, ctx context.Context, port int, path string, want int) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == want {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("%s never returned %d (last error: %v)", path, want, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func unpublishedEvents(t *testing.T, ctx context.Context, databaseURL, walletID string) int64 {
	t.Helper()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var pending int64
	err = pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND
		(aggregate_id=$1 OR aggregate_id IN (SELECT id FROM wager_transactions WHERE wallet_id=$1))`, walletID).Scan(&pending)
	if err != nil {
		t.Fatal(err)
	}
	return pending
}

type switchTCPProxy struct {
	listener  net.Listener
	target    string
	available atomic.Bool
	mu        sync.Mutex
	conns     map[net.Conn]struct{}
}

func newSwitchTCPProxy(t *testing.T, target string) *switchTCPProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &switchTCPProxy{listener: listener, target: target, conns: make(map[net.Conn]struct{})}
	proxy.available.Store(true)
	go proxy.run()
	t.Cleanup(func() {
		proxy.listener.Close()
		proxy.setAvailable(false)
	})
	return proxy
}

func (p *switchTCPProxy) address() string { return p.listener.Addr().String() }

func (p *switchTCPProxy) setAvailable(value bool) {
	p.available.Store(value)
	if value {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.conns {
		conn.Close()
	}
}

func (p *switchTCPProxy) run() {
	for {
		client, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.forward(client)
	}
}

func (p *switchTCPProxy) forward(client net.Conn) {
	if !p.available.Load() {
		client.Close()
		return
	}
	server, err := net.DialTimeout("tcp", p.target, time.Second)
	if err != nil {
		client.Close()
		return
	}
	p.mu.Lock()
	p.conns[client] = struct{}{}
	p.conns[server] = struct{}{}
	if !p.available.Load() {
		client.Close()
		server.Close()
	}
	p.mu.Unlock()
	var once sync.Once
	closePair := func() {
		once.Do(func() {
			client.Close()
			server.Close()
			p.mu.Lock()
			delete(p.conns, client)
			delete(p.conns, server)
			p.mu.Unlock()
		})
	}
	go func() { io.Copy(server, client); closePair() }()
	io.Copy(client, server)
	closePair()
}

type switchSQSProxy struct {
	URL       string
	available atomic.Bool
}

func newSwitchSQSProxy(t *testing.T, target string) *switchSQSProxy {
	t.Helper()
	upstream, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &switchSQSProxy{}
	reverse := httputil.NewSingleHostReverseProxy(upstream)
	reverse.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if r.Context().Err() == nil {
			http.Error(w, "SQS proxy upstream unavailable", http.StatusBadGateway)
		}
	}
	reverse.ModifyResponse = func(response *http.Response) error {
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return err
		}
		// LocalStack returns absolute queue URLs. Route subsequent calls through
		// this same fault point rather than bypassing it.
		for _, base := range []string{upstream.String(), "http://sqs.us-east-1.localhost.localstack.cloud:4566"} {
			body = bytes.ReplaceAll(body, []byte(base), []byte(proxy.URL))
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		response.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !proxy.available.Load() {
			http.Error(w, "temporary SQS outage", http.StatusServiceUnavailable)
			return
		}
		reverse.ServeHTTP(w, r)
	}))
	proxy.URL = server.URL
	proxy.available.Store(true)
	t.Cleanup(server.Close)
	return proxy
}
