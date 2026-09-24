package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reconciliation"
)

type ReconciliationStore struct{ unit *UnitOfWork }

func NewReconciliationStore(unit *UnitOfWork) *ReconciliationStore {
	return &ReconciliationStore{unit: unit}
}

func (s *ReconciliationStore) Snapshot(ctx context.Context, walletID string) (application.Snapshot, error) {
	var snapshot application.Snapshot
	err := s.unit.RepeatableReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT wallet.balance_minor, wallet.currency,
			       COALESCE(SUM(CASE WHEN entry.direction='CREDIT'
			           THEN entry.amount_minor::numeric ELSE -entry.amount_minor::numeric END), 0)::text,
			       COUNT(entry.id)
			FROM wallets AS wallet
			LEFT JOIN wallet_ledger_entries AS entry ON entry.wallet_id=wallet.id
			WHERE wallet.id=$1
			GROUP BY wallet.id`, walletID).Scan(
			&snapshot.StoredMinor, &snapshot.Currency, &snapshot.CalculatedMinor, &snapshot.CheckedEntries,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Snapshot{}, application.ErrWalletNotFound
	}
	if err != nil {
		return application.Snapshot{}, fmt.Errorf("read reconciliation snapshot: %w", err)
	}
	return snapshot, nil
}

var _ application.Store = (*ReconciliationStore)(nil)
