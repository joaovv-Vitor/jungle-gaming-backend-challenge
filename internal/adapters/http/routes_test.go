package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
)

func TestHealthRoutes(t *testing.T) {
	status := health.New()
	mux := newMux(status)

	assertStatus(t, mux, "/health/live", http.StatusOK)
	assertStatus(t, mux, "/health/ready", http.StatusServiceUnavailable)

	status.SetReady(true)
	assertStatus(t, mux, "/health/ready", http.StatusOK)
}

func assertStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != want {
		t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, want)
	}
}
