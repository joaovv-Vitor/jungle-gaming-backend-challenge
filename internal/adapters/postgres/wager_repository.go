package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
)

type WagerRepository struct{}

func NewWagerRepository() *WagerRepository { return &WagerRepository{} }

func (r *WagerRepository) Insert(ctx context.Context, db DBTX, entity *wagering.Transaction, schedule *ReferenceSchedule) error {
	if entity.Status() == wagering.StatusPendingReference {
		if schedule == nil || !schedule.valid() {
			return ErrInvalidSchedule
		}
	} else if schedule != nil {
		return ErrInvalidSchedule
	}
	result, hasResult := entity.ResultBalance()
	var resultMinor any
	if hasResult {
		resultMinor = result.MinorUnits()
	}
	var payloadHash any
	if entity.Origin() == wagering.OriginExternal {
		hash := entity.PayloadHash()
		payloadHash = hash[:]
	}
	var nextAttemptAt, expiresAt any
	if schedule != nil {
		nextAttemptAt = schedule.NextAttemptAt.UTC()
		expiresAt = schedule.ExpiresAt.UTC()
	}
	_, err := db.Exec(ctx, `
        INSERT INTO wager_transactions(
            id, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
            wallet_id, player_id, round_id, game_id, kind, status, amount_minor, currency,
            reference_external_transaction_id, reference_transaction_id, failure_code,
            result_balance_minor, next_attempt_at, expires_at, created_at, updated_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		entity.ID(), entity.Origin(), nullString(entity.ProviderID()), nullString(entity.ExternalTransactionID()),
		nullString(entity.IdempotencyKey()), payloadHash, entity.WalletID(), entity.PlayerID(), nullString(entity.RoundID()),
		nullString(entity.GameID()), entity.Kind(), entity.Status(), entity.Amount().MinorUnits(), entity.Amount().Currency().Code(),
		nullString(entity.ReferenceExternalTransactionID()), nullString(entity.ReferenceTransactionID()),
		nullString(string(entity.FailureCode())), resultMinor, nextAttemptAt, expiresAt, entity.CreatedAt(), entity.UpdatedAt())
	if err != nil {
		return fmt.Errorf("insert wager transaction: %w", err)
	}
	return nil
}

func (r *WagerRepository) FindByID(ctx context.Context, db DBTX, id string) (*wagering.Transaction, error) {
	return scanWager(db.QueryRow(ctx, wagerSelect+` WHERE id=$1`, id))
}

func (r *WagerRepository) FindByProviderIdempotencyKey(ctx context.Context, db DBTX, providerID, key string) (*wagering.Transaction, error) {
	return scanWager(db.QueryRow(ctx, wagerSelect+` WHERE provider_id=$1 AND idempotency_key=$2`, providerID, key))
}

func (r *WagerRepository) FindByProviderExternalID(ctx context.Context, db DBTX, providerID, externalID string) (*wagering.Transaction, error) {
	return scanWager(db.QueryRow(ctx, wagerSelect+` WHERE provider_id=$1 AND external_transaction_id=$2`, providerID, externalID))
}

const wagerSelect = `SELECT
    id::text, origin, provider_id, external_transaction_id, idempotency_key, payload_hash,
    wallet_id::text, player_id::text, round_id, game_id, kind, status, amount_minor, currency,
    reference_external_transaction_id, reference_transaction_id::text, failure_code,
    result_balance_minor, created_at, updated_at
    FROM wager_transactions`

func scanWager(row pgx.Row) (*wagering.Transaction, error) {
	var state wagering.Rehydration
	var providerID, externalID, idempotencyKey, roundID, gameID pgtype.Text
	var referenceExternalID, referenceID, failureCode pgtype.Text
	var resultMinor pgtype.Int8
	var payloadHash []byte
	var amountMinor int64
	var currencyCode string
	err := row.Scan(
		&state.ID, &state.Origin, &providerID, &externalID, &idempotencyKey, &payloadHash,
		&state.WalletID, &state.PlayerID, &roundID, &gameID, &state.Kind, &state.Status, &amountMinor, &currencyCode,
		&referenceExternalID, &referenceID, &failureCode, &resultMinor, &state.CreatedAt, &state.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan wager transaction: %w", err)
	}
	state.ProviderID = textValue(providerID)
	state.ExternalTransactionID = textValue(externalID)
	state.IdempotencyKey = textValue(idempotencyKey)
	state.RoundID = textValue(roundID)
	state.GameID = textValue(gameID)
	state.ReferenceExternalTransactionID = textValue(referenceExternalID)
	state.ReferenceTransactionID = textValue(referenceID)
	state.FailureCode = wagering.FailureCode(textValue(failureCode))
	if len(payloadHash) > 0 {
		if len(payloadHash) != len(state.PayloadHash) {
			return nil, fmt.Errorf("map payload hash: unexpected length %d", len(payloadHash))
		}
		copy(state.PayloadHash[:], payloadHash)
	}
	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return nil, fmt.Errorf("map wager currency: %w", err)
	}
	state.Amount, err = money.New(amountMinor, currency)
	if err != nil {
		return nil, fmt.Errorf("map wager amount: %w", err)
	}
	if resultMinor.Valid {
		result, err := money.New(resultMinor.Int64, currency)
		if err != nil {
			return nil, fmt.Errorf("map wager result: %w", err)
		}
		state.ResultBalance = &result
	}
	entity, err := wagering.Rehydrate(state)
	if err != nil {
		return nil, fmt.Errorf("rehydrate wager transaction: %w", err)
	}
	return entity, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
