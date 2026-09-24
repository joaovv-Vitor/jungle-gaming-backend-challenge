package httpadapter

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/auth"
	applicationreconciliation "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reconciliation"
	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/health"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

func newMux(
	status *health.Status,
	instrumentation *metrics.Metrics,
	authentication *auth.Middleware,
	wallets *applicationwallet.Service,
	wagers *applicationwagering.Service,
	reconciler *applicationreconciliation.Service,
	logger *slog.Logger,
) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", authentication.RequireRole("internal", instrumentation.Handler()))
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "up"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := status.Ready(r.Context()); err != nil {
			var dependency *health.DependencyError
			if errors.As(err, &dependency) {
				instrumentation.RecordDependencyFailure(dependency.Name)
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	registerWalletRoutes(mux, authentication, wallets, reconciler, instrumentation, logger)
	registerWagerRoutes(mux, authentication, wagers, instrumentation, logger)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
