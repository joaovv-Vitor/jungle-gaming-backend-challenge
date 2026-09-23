package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/reference"
)

type ReferenceStore struct {
	unit   *UnitOfWork
	wagers *WagerStore
}

func NewReferenceStore(
	unit *UnitOfWork, wallets *WalletRepository, wagers *WagerRepository,
	ledger *LedgerRepository, outbox *OutboxRepository,
) *ReferenceStore {
	return &ReferenceStore{unit: unit, wagers: NewWagerStore(unit, wallets, wagers, ledger, outbox)}
}

func (s *ReferenceStore) Claim(ctx context.Context, lease time.Duration) (*application.Claim, error) {
	if lease <= 0 {
		return nil, application.ErrInvalidClaim
	}
	var claim *application.Claim
	err := s.unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var value application.Claim
		err := tx.QueryRow(ctx, `
			WITH candidate AS (
				SELECT id FROM wager_transactions
				WHERE status='PENDING_REFERENCE'
				  AND next_attempt_at <= statement_timestamp()
				  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
				ORDER BY next_attempt_at, id
				FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE wager_transactions AS wager
			SET lease_token=gen_random_uuid(),
				locked_until=statement_timestamp() + ($1::bigint * interval '1 millisecond'),
				next_attempt_at=LEAST(
					statement_timestamp() + ($1::bigint * interval '1 millisecond'), expires_at)
			FROM candidate WHERE wager.id=candidate.id
			RETURNING wager.id::text, wager.wallet_id::text, wager.lease_token::text`,
			lease.Milliseconds()).Scan(&value.TransactionID, &value.WalletID, &value.Token)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim pending reference: %w", err)
		}
		claim = &value
		return nil
	})
	return claim, err
}

func (s *ReferenceStore) WithinTransaction(ctx context.Context, work func(application.Session) error) error {
	return s.unit.ReadCommittedWithRetry(ctx, func(ctx context.Context, tx pgx.Tx) error {
		session := &referenceSession{wagerSession: wagerSession{tx: tx, store: s.wagers}}
		return work(session)
	})
}

type referenceSession struct {
	wagerSession
}

func (s *referenceSession) LockPending(ctx context.Context, claim application.Claim) (*application.Pending, error) {
	transaction, err := scanWager(s.tx.QueryRow(ctx, wagerSelect+`
		WHERE id=$1 AND status='PENDING_REFERENCE' AND lease_token=$2
		  AND locked_until > clock_timestamp() FOR UPDATE`, claim.TransactionID, claim.Token))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if transaction.WalletID() != claim.WalletID {
		return nil, application.ErrInvalidClaim
	}
	var pending application.Pending
	err = s.tx.QueryRow(ctx, `
		SELECT reference_attempts, expires_at, clock_timestamp()
		FROM wager_transactions WHERE id=$1 AND lease_token=$2`, claim.TransactionID, claim.Token).
		Scan(&pending.Attempts, &pending.ExpiresAt, &pending.Now)
	if err != nil {
		return nil, fmt.Errorf("read pending reference schedule: %w", err)
	}
	pending.Transaction = transaction
	s.pendingToken = claim.Token
	return &pending, nil
}

func (s *referenceSession) Reschedule(ctx context.Context, claim application.Claim, next time.Time) error {
	if s.pendingToken != claim.Token || next.IsZero() {
		return application.ErrInvalidClaim
	}
	tag, err := s.tx.Exec(ctx, `
		UPDATE wager_transactions SET reference_attempts=reference_attempts+1,
			next_attempt_at=$1, lease_token=NULL, locked_until=NULL
		WHERE id=$2 AND status='PENDING_REFERENCE' AND lease_token=$3`,
		next.UTC(), claim.TransactionID, claim.Token)
	if err != nil {
		return fmt.Errorf("reschedule pending reference: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentWrite
	}
	return nil
}

var _ application.Store = (*ReferenceStore)(nil)
var _ application.Session = (*referenceSession)(nil)
