package postgres

import (
	"context"
	"fmt"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/ledger"
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
