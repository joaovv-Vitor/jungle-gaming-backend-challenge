package httpadapter

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/auth"
	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	platformid "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/id"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

func registerWagerRoutes(mux *http.ServeMux, authentication *auth.Middleware, wagers *application.Service, instrumentation *metrics.Metrics, logger *slog.Logger) {
	handler := wagerHandler{wagers: wagers, metrics: instrumentation, logger: logger}
	mux.Handle("POST /wagering/transactions", authentication.RequireRole("provider", http.HandlerFunc(handler.submit)))
	mux.Handle("GET /wagering/transactions/{transactionId}", authentication.RequireRole("provider", http.HandlerFunc(handler.findByID)))
	mux.Handle("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", authentication.RequireRole("provider", http.HandlerFunc(handler.findByExternalID)))
}

type wagerHandler struct {
	wagers  *application.Service
	metrics *metrics.Metrics
	logger  *slog.Logger
}

type submitWagerRequest struct {
	ProviderID                     string       `json:"providerId"`
	ExternalTransactionID          string       `json:"externalTransactionId"`
	PlayerID                       string       `json:"playerId"`
	WalletID                       string       `json:"walletId"`
	RoundID                        string       `json:"roundId"`
	GameID                         string       `json:"gameId"`
	Kind                           domain.Kind  `json:"kind"`
	Money                          moneyRequest `json:"money"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
}

func (h wagerHandler) submit(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok || identity.ProviderID == "" {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN")
		return
	}
	var request submitWagerRequest
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if request.ProviderID != identity.ProviderID {
		writeAPIError(w, http.StatusForbidden, "PROVIDER_MISMATCH")
		return
	}
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeAPIError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED")
		return
	}
	amount, err := money.ParseNonNegative(request.Money.Amount, request.Money.Currency)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_MONEY")
		return
	}
	correlationID := r.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID, err = platformid.New()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
			return
		}
	}
	started := time.Now()
	result, err := h.wagers.Submit(r.Context(), application.SubmitInput{
		ProviderID: request.ProviderID, ExternalTransactionID: request.ExternalTransactionID,
		IdempotencyKey: idempotencyKey, WalletID: request.WalletID, PlayerID: request.PlayerID,
		RoundID: request.RoundID, GameID: request.GameID, Kind: request.Kind, Amount: amount,
		ReferenceExternalTransactionID: request.ReferenceExternalTransactionID,
		CorrelationID:                  correlationID,
	})
	if err != nil {
		code := wagerErrorMetric(err)
		h.metrics.RecordWagerError("http", code)
		h.logger.Warn("wager submission failed", "correlationId", correlationID,
			"providerId", request.ProviderID, "walletId", request.WalletID,
			"externalTransactionId", request.ExternalTransactionID, "reason", code)
		writeWagerError(w, err)
		return
	}
	h.metrics.RecordWager("http", string(result.Transaction.Kind()), string(result.Transaction.Status()),
		string(result.Transaction.FailureCode()), result.Replay, time.Since(started))
	h.logger.Info("wager submission handled", "correlationId", correlationID,
		"transactionId", result.Transaction.ID(), "externalTransactionId", request.ExternalTransactionID,
		"providerId", request.ProviderID, "walletId", request.WalletID,
		"status", result.Transaction.Status(), "replay", result.Replay)
	writeWagerResponse(w, result.Transaction, result.Replay)
}

func wagerErrorMetric(err error) string {
	switch {
	case errors.Is(err, application.ErrIdempotencyConflict):
		return "idempotency_conflict"
	case errors.Is(err, application.ErrExternalIDConflict):
		return "external_id_conflict"
	case errors.Is(err, application.ErrConcurrentWrite), errors.Is(err, application.ErrIdentityRace):
		return "concurrent_write"
	case isTransientDependencyError(err):
		return "dependency_unavailable"
	case errors.Is(err, application.ErrInvalidInput):
		return "invalid"
	default:
		return "error"
	}
}

func (h wagerHandler) findByID(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok || identity.ProviderID == "" {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN")
		return
	}
	transaction, err := h.wagers.FindByID(r.Context(), identity.ProviderID, r.PathValue("transactionId"))
	if err != nil {
		writeWagerError(w, err)
		return
	}
	writeWagerResponse(w, transaction, false)
}

func (h wagerHandler) findByExternalID(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok || identity.ProviderID == "" {
		writeAPIError(w, http.StatusForbidden, "FORBIDDEN")
		return
	}
	if r.PathValue("providerId") != identity.ProviderID {
		writeAPIError(w, http.StatusForbidden, "PROVIDER_MISMATCH")
		return
	}
	transaction, err := h.wagers.FindByExternalID(r.Context(), identity.ProviderID, r.PathValue("externalTransactionId"))
	if err != nil {
		writeWagerError(w, err)
		return
	}
	writeWagerResponse(w, transaction, false)
}

func writeWagerResponse(w http.ResponseWriter, transaction *domain.Transaction, replay bool) {
	status := http.StatusOK
	if transaction.Status() == domain.StatusPendingReference || transaction.Status() == domain.StatusPending {
		status = http.StatusAccepted
	}
	response := struct {
		TransactionID    string             `json:"transactionId"`
		Status           domain.Status      `json:"status"`
		FailureCode      domain.FailureCode `json:"failureCode,omitempty"`
		Balance          *money.Money       `json:"balance,omitempty"`
		IdempotentReplay bool               `json:"idempotentReplay"`
	}{
		TransactionID: transaction.ID(), Status: transaction.Status(),
		FailureCode: transaction.FailureCode(), IdempotentReplay: replay,
	}
	if balance, present := transaction.ResultBalance(); present {
		response.Balance = &balance
	}
	writeJSON(w, status, response)
}

func writeWagerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, application.ErrInvalidInput):
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST")
	case errors.Is(err, application.ErrWalletNotFound):
		writeAPIError(w, http.StatusNotFound, "WALLET_NOT_FOUND")
	case errors.Is(err, application.ErrWalletMismatch):
		writeAPIError(w, http.StatusUnprocessableEntity, "WALLET_MISMATCH")
	case errors.Is(err, application.ErrIdempotencyConflict):
		writeAPIError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
	case errors.Is(err, application.ErrExternalIDConflict):
		writeAPIError(w, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT")
	case errors.Is(err, application.ErrTransactionNotFound):
		writeAPIError(w, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	case errors.Is(err, application.ErrConcurrentWrite), errors.Is(err, application.ErrIdentityRace):
		writeAPIError(w, http.StatusServiceUnavailable, "TRANSIENT_FAILURE")
	default:
		writeDependencyOrInternalError(w, err)
	}
}
