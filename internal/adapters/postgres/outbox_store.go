package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
)

type OutboxStore struct{ unit *UnitOfWork }

func NewOutboxStore(unit *UnitOfWork) *OutboxStore { return &OutboxStore{unit: unit} }

func (s *OutboxStore) Stats(ctx context.Context) (application.Stats, error) {
	var stats application.Stats
	err := s.unit.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(GREATEST(0,
			EXTRACT(EPOCH FROM (clock_timestamp()-MIN(occurred_at)))), 0)::double precision
		FROM outbox_events WHERE published_at IS NULL`).Scan(&stats.Pending, &stats.OldestAgeSeconds)
	if err != nil {
		return application.Stats{}, fmt.Errorf("read outbox backlog: %w", err)
	}
	return stats, nil
}

func (s *OutboxStore) Claim(ctx context.Context, lease time.Duration) (*application.Event, error) {
	if lease <= 0 {
		return nil, application.ErrInvalidClaim
	}
	var event *application.Event
	err := s.unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var claimed application.Event
		err := tx.QueryRow(ctx, `
			WITH candidate AS (
				SELECT event_id FROM outbox_events
				WHERE published_at IS NULL AND next_attempt_at <= statement_timestamp()
				  AND (locked_until IS NULL OR locked_until <= statement_timestamp())
				ORDER BY next_attempt_at, occurred_at, event_id
				FOR UPDATE SKIP LOCKED LIMIT 1
			), claimed AS (
				UPDATE outbox_events AS outbox
				SET lease_token=gen_random_uuid(),
				    locked_until=statement_timestamp() + ($1::bigint * interval '1 millisecond'),
				    next_attempt_at=statement_timestamp() + ($1::bigint * interval '1 millisecond'),
				    attempts=attempts+1
				FROM candidate WHERE outbox.event_id=candidate.event_id
				RETURNING outbox.event_id, outbox.aggregate_id, outbox.event_type, outbox.correlation_id,
				          outbox.payload, outbox.lease_token, outbox.attempts
			)
			SELECT claimed.event_id::text,
			       CASE WHEN claimed.event_type='WalletBalanceChanged'
			            THEN claimed.aggregate_id::text ELSE wager.wallet_id::text END,
			       claimed.correlation_id, claimed.payload, claimed.lease_token::text, claimed.attempts
			FROM claimed LEFT JOIN wager_transactions AS wager ON wager.id=claimed.aggregate_id`,
			lease.Milliseconds()).Scan(&claimed.ID, &claimed.GroupID, &claimed.CorrelationID, &claimed.Payload, &claimed.Token, &claimed.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim outbox event: %w", err)
		}
		event = &claimed
		return nil
	})
	return event, err
}

func (s *OutboxStore) Confirm(ctx context.Context, event application.Event) error {
	tag, err := s.unit.pool.Exec(ctx, `
		UPDATE outbox_events SET published_at=clock_timestamp(), lease_token=NULL,
			locked_until=NULL, last_error=NULL
		WHERE event_id=$1 AND lease_token=$2 AND published_at IS NULL`, event.ID, event.Token)
	if err != nil {
		return fmt.Errorf("confirm outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentWrite
	}
	return nil
}

func (s *OutboxStore) Retry(ctx context.Context, event application.Event, next time.Time, reason string) error {
	if next.IsZero() {
		return application.ErrInvalidClaim
	}
	tag, err := s.unit.pool.Exec(ctx, `
		UPDATE outbox_events SET next_attempt_at=$1, lease_token=NULL,
			locked_until=NULL, last_error=$2
		WHERE event_id=$3 AND lease_token=$4 AND published_at IS NULL`,
		next.UTC(), reason, event.ID, event.Token)
	if err != nil {
		return fmt.Errorf("retry outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConcurrentWrite
	}
	return nil
}

var _ application.Store = (*OutboxStore)(nil)
