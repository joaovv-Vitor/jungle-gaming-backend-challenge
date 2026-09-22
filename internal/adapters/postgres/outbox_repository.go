package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/event"
)

type OutboxRepository struct{}

func NewOutboxRepository() *OutboxRepository { return &OutboxRepository{} }

func (r *OutboxRepository) Insert(ctx context.Context, db DBTX, domainEvent event.IntegrationEvent) error {
	payload, err := json.Marshal(domainEvent)
	if err != nil {
		return fmt.Errorf("marshal outbox event: %w", err)
	}
	_, err = db.Exec(ctx, `
		INSERT INTO outbox_events(
			event_id, aggregate_id, event_type, event_version, correlation_id,
			causation_id, payload, occurred_at, next_attempt_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)`,
		domainEvent.ID(), domainEvent.AggregateID(), domainEvent.Type(), domainEvent.Version(),
		domainEvent.CorrelationID(), nullString(domainEvent.CausationID()), payload, domainEvent.OccurredAt())
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", err)
	}
	return nil
}
