package httpadapter

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/auth"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	platformid "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/id"
)

func registerWalletRoutes(mux *http.ServeMux, authentication *auth.Middleware, wallets *applicationwallet.Service) {
	handler := walletHandler{wallets: wallets}
	mux.Handle("POST /wallets", authentication.RequireRole("internal", http.HandlerFunc(handler.open)))
	mux.Handle("GET /wallets/{walletId}", authentication.RequireRole("internal", http.HandlerFunc(handler.find)))
	mux.Handle("GET /wallets/{walletId}/ledger", authentication.RequireRole("internal", http.HandlerFunc(handler.listLedger)))
}

type walletHandler struct {
	wallets *applicationwallet.Service
}

type moneyRequest struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type openWalletRequest struct {
	PlayerID       string       `json:"playerId"`
	InitialBalance moneyRequest `json:"initialBalance"`
}

func (h walletHandler) open(w http.ResponseWriter, r *http.Request) {
	var request openWalletRequest
	if err := decodeJSON(r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	initial, err := money.ParseNonNegative(request.InitialBalance.Amount, request.InitialBalance.Currency)
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
	account, err := h.wallets.Open(r.Context(), applicationwallet.OpenInput{
		PlayerID: request.PlayerID, Initial: initial, CorrelationID: correlationID,
	})
	if errors.Is(err, applicationwallet.ErrWalletAlreadyExists) {
		writeAPIError(w, http.StatusConflict, "WALLET_ALREADY_EXISTS")
		return
	}
	if err != nil {
		if errors.Is(err, applicationwallet.ErrInvalidInput) {
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	w.Header().Set("Location", "/wallets/"+account.ID())
	writeJSON(w, http.StatusCreated, walletResponse(account))
}

func (h walletHandler) find(w http.ResponseWriter, r *http.Request) {
	account, err := h.wallets.FindByID(r.Context(), r.PathValue("walletId"))
	if errors.Is(err, applicationwallet.ErrWalletNotFound) {
		writeAPIError(w, http.StatusNotFound, "WALLET_NOT_FOUND")
		return
	}
	if err != nil {
		if errors.Is(err, applicationwallet.ErrInvalidInput) {
			writeAPIError(w, http.StatusBadRequest, "INVALID_WALLET_ID")
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	writeJSON(w, http.StatusOK, walletResponse(account))
}

func (h walletHandler) listLedger(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_PAGINATION")
			return
		}
		limit = parsed
	}
	page, err := h.wallets.ListLedger(r.Context(), r.PathValue("walletId"), r.URL.Query().Get("cursor"), limit)
	if errors.Is(err, applicationwallet.ErrWalletNotFound) {
		writeAPIError(w, http.StatusNotFound, "WALLET_NOT_FOUND")
		return
	}
	if errors.Is(err, applicationwallet.ErrInvalidInput) {
		writeAPIError(w, http.StatusBadRequest, "INVALID_PAGINATION")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	type entryResponse struct {
		ID            string      `json:"id"`
		TransactionID string      `json:"transactionId"`
		Direction     string      `json:"direction"`
		Money         money.Money `json:"money"`
		BalanceBefore money.Money `json:"balanceBefore"`
		BalanceAfter  money.Money `json:"balanceAfter"`
		WalletVersion int64       `json:"walletVersion"`
		CreatedAt     time.Time   `json:"createdAt"`
	}
	entries := make([]entryResponse, 0, len(page.Entries))
	for _, entry := range page.Entries {
		entries = append(entries, entryResponse{
			ID: entry.ID(), TransactionID: entry.TransactionID(), Direction: string(entry.Direction()),
			Money: entry.Amount(), BalanceBefore: entry.BalanceBefore(), BalanceAfter: entry.BalanceAfter(),
			WalletVersion: entry.WalletVersion(), CreatedAt: entry.CreatedAt(),
		})
	}
	writeJSON(w, http.StatusOK, struct {
		WalletID   string          `json:"walletId"`
		Entries    []entryResponse `json:"entries"`
		NextCursor string          `json:"nextCursor,omitempty"`
	}{WalletID: r.PathValue("walletId"), Entries: entries, NextCursor: page.NextCursor})
}

func walletResponse(account interface {
	ID() string
	PlayerID() string
	Balance() money.Money
	Version() int64
}) any {
	return struct {
		ID       string      `json:"id"`
		PlayerID string      `json:"playerId"`
		Balance  money.Money `json:"balance"`
		Version  int64       `json:"version"`
	}{account.ID(), account.PlayerID(), account.Balance(), account.Version()}
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"code": code})
}
