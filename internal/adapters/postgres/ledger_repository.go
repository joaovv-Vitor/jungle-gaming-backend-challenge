package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
)

type LedgerRepository struct{}

func NewLedgerRepository() *LedgerRepository { return &LedgerRepository{} }

func (r *LedgerRepository) Insert(ctx context.Context, db DBTX, entry *ledger.Entry) error {
	_, err := db.Exec(ctx, `
        INSERT INTO wallet_ledger_entries(
            id, wallet_id, transaction_id, direction, amount_minor, currency,
            balance_before_minor, balance_after_minor, wallet_version, created_at
        ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		entry.ID(), entry.WalletID(), entry.TransactionID(), entry.Direction(), entry.Amount().MinorUnits(),
		entry.Amount().Currency().Code(), entry.BalanceBefore().MinorUnits(), entry.BalanceAfter().MinorUnits(),
		entry.WalletVersion(), entry.CreatedAt())
	if err != nil {
		return fmt.Errorf("insert ledger entry: %w", err)
	}
	return nil
}

type LedgerCursor struct {
	WalletVersion int64
	EntryID       string
}

func (r *LedgerRepository) ListByWallet(
	ctx context.Context,
	db DBTX,
	walletID string,
	after *LedgerCursor,
	limit int,
) ([]*ledger.Entry, error) {
	query := `SELECT id::text, wallet_id::text, transaction_id::text, direction,
		amount_minor, currency, balance_before_minor, balance_after_minor, wallet_version, created_at
		FROM wallet_ledger_entries WHERE wallet_id=$1`
	arguments := []any{walletID}
	if after != nil {
		query += ` AND (wallet_version, id) < ($2, $3)`
		arguments = append(arguments, after.WalletVersion, after.EntryID)
	}
	query += fmt.Sprintf(` ORDER BY wallet_version DESC, id DESC LIMIT $%d`, len(arguments)+1)
	arguments = append(arguments, limit)
	rows, err := db.Query(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list wallet ledger: %w", err)
	}
	defer rows.Close()
	entries := make([]*ledger.Entry, 0, limit)
	for rows.Next() {
		entry, err := scanLedgerEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wallet ledger: %w", err)
	}
	return entries, nil
}

func scanLedgerEntry(row pgx.Row) (*ledger.Entry, error) {
	var params ledger.Params
	var direction ledger.Direction
	var amountMinor, beforeMinor, afterMinor int64
	var currencyCode string
	err := row.Scan(
		&params.ID, &params.WalletID, &params.TransactionID, &direction,
		&amountMinor, &currencyCode, &beforeMinor, &afterMinor, &params.WalletVersion, &params.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan wallet ledger: %w", err)
	}
	currency, err := money.ParseCurrency(currencyCode)
	if err != nil {
		return nil, fmt.Errorf("map ledger currency: %w", err)
	}
	params.Direction = direction
	params.Amount, err = money.New(amountMinor, currency)
	if err != nil {
		return nil, err
	}
	params.BalanceBefore, err = money.New(beforeMinor, currency)
	if err != nil {
		return nil, err
	}
	params.BalanceAfter, err = money.New(afterMinor, currency)
	if err != nil {
		return nil, err
	}
	entry, err := ledger.Rehydrate(params)
	if err != nil {
		return nil, fmt.Errorf("rehydrate ledger entry: %w", err)
	}
	return entry, nil
}
