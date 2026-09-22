package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wallet"
)

type WalletRepository struct{}

func NewWalletRepository() *WalletRepository { return &WalletRepository{} }

func (r *WalletRepository) Insert(ctx context.Context, db DBTX, entity *wallet.Wallet) error {
	_, err := db.Exec(ctx, `
        INSERT INTO wallets(id, player_id, currency, balance_minor, version, created_at, updated_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		entity.ID(), entity.PlayerID(), entity.Currency().Code(), entity.Balance().MinorUnits(),
		entity.Version(), entity.CreatedAt(), entity.UpdatedAt())
	if err != nil {
		return fmt.Errorf("insert wallet: %w", err)
	}
	return nil
}

func (r *WalletRepository) FindByID(ctx context.Context, db DBTX, id string) (*wallet.Wallet, error) {
	return r.find(ctx, db, id, false)
}

func (r *WalletRepository) FindByIDForUpdate(ctx context.Context, db DBTX, id string) (*wallet.Wallet, error) {
	return r.find(ctx, db, id, true)
}

func (r *WalletRepository) find(ctx context.Context, db DBTX, id string, lock bool) (*wallet.Wallet, error) {
	query := `SELECT id::text, player_id::text, currency, balance_minor, version, created_at, updated_at FROM wallets WHERE id=$1`
	if lock {
		query += ` FOR NO KEY UPDATE`
	}
	var state wallet.Rehydration
	var currencyCode string
	var balanceMinor int64
	err := db.QueryRow(ctx, query, id).Scan(
		&state.ID, &state.PlayerID, &currencyCode, &balanceMinor, &state.Version, &state.CreatedAt, &state.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find wallet: %w", err)
	}
	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return nil, fmt.Errorf("map wallet currency: %w", err)
	}
	state.Balance, err = money.New(balanceMinor, currency)
	if err != nil {
		return nil, fmt.Errorf("map wallet balance: %w", err)
	}
	entity, err := wallet.Rehydrate(state)
	if err != nil {
		return nil, fmt.Errorf("rehydrate wallet: %w", err)
	}
	return entity, nil
}

func (r *WalletRepository) Update(ctx context.Context, db DBTX, entity *wallet.Wallet) error {
	if entity.Version() <= 1 {
		return ErrConcurrentWrite
	}
	tag, err := db.Exec(ctx, `
        UPDATE wallets
        SET balance_minor=$1, version=$2, updated_at=$3
        WHERE id=$4 AND version=$5`,
		entity.Balance().MinorUnits(), entity.Version(), entity.UpdatedAt(), entity.ID(), entity.Version()-1)
	if err != nil {
		return fmt.Errorf("update wallet: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentWrite
	}
	return nil
}
