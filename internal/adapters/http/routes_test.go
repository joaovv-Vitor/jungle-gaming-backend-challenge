package httpadapter

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/auth"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/ledger"
	walletdomain "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
)

func TestHealthRoutes(t *testing.T) {
	status := health.New()
	mux := newMux(status, auth.NewMiddlewareWithVerifier(routeVerifier{}), applicationwallet.NewService(routeWalletStore{}))

	assertStatus(t, mux, "/health/live", http.StatusOK)
	assertStatus(t, mux, "/health/ready", http.StatusServiceUnavailable)

	status.SetReady(true)
	assertStatus(t, mux, "/health/ready", http.StatusOK)
}

func TestWalletRoutesEnforceAuthenticationAndInternalRole(t *testing.T) {
	mux := newMux(health.New(), auth.NewMiddlewareWithVerifier(routeVerifier{}), applicationwallet.NewService(routeWalletStore{}))
	body := []byte(`{"playerId":"10000000-0000-4000-8000-000000000001","initialBalance":{"amount":"100.00","currency":"BRL"}}`)

	for _, test := range []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "missing token", wantStatus: http.StatusUnauthorized},
		{name: "invalid token", token: "invalid", wantStatus: http.StatusUnauthorized},
		{name: "provider forbidden", token: "provider", wantStatus: http.StatusForbidden},
		{name: "internal allowed", token: "internal", wantStatus: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/wallets", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

type routeVerifier struct{}

func (routeVerifier) Verify(_ context.Context, token string) (auth.Identity, error) {
	switch token {
	case "internal":
		return auth.NewIdentity("internal-subject", "internal-service", "", []string{"internal"}), nil
	case "provider":
		return auth.NewIdentity("provider-subject", "provider-a", "provider-a", []string{"provider"}), nil
	default:
		return auth.Identity{}, errors.New("invalid token")
	}
}

type routeWalletStore struct{}

func (routeWalletStore) Create(context.Context, applicationwallet.Creation) error { return nil }
func (routeWalletStore) FindByID(context.Context, string) (*walletdomain.Wallet, error) {
	return nil, applicationwallet.ErrWalletNotFound
}
func (routeWalletStore) ListLedger(context.Context, string, *applicationwallet.LedgerCursor, int) ([]*ledger.Entry, error) {
	return nil, nil
}

func assertStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != want {
		t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, want)
	}
}
