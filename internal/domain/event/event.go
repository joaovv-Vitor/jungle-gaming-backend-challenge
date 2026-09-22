package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidEvent = errors.New("invalid integration event")

const ContractVersion = 1

type Type string

const (
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

type IntegrationEvent interface {
	ID() string
	Type() Type
	AggregateID() string
	CorrelationID() string
	CausationID() string
	OccurredAt() time.Time
	Version() int
	json.Marshaler
}

type Metadata struct {
	EventID       string
	AggregateID   string
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

type Envelope[T any] struct {
	eventID       string
	eventType     Type
	aggregateID   string
	correlationID string
	causationID   string
	occurredAt    time.Time
	version       int
	data          T
}

func newEnvelope[T any](metadata Metadata, eventType Type, data T) (Envelope[T], error) {
	if !validID(metadata.EventID) || !validID(metadata.AggregateID) || !validID(metadata.CorrelationID) {
		return Envelope[T]{}, fmt.Errorf("%w: event, aggregate and correlation ids are required", ErrInvalidEvent)
	}
	if metadata.CausationID != "" && !validID(metadata.CausationID) {
		return Envelope[T]{}, fmt.Errorf("%w: causation id", ErrInvalidEvent)
	}
	if metadata.OccurredAt.IsZero() {
		return Envelope[T]{}, fmt.Errorf("%w: occurred at", ErrInvalidEvent)
	}

	return Envelope[T]{
		eventID:       metadata.EventID,
		eventType:     eventType,
		aggregateID:   metadata.AggregateID,
		correlationID: metadata.CorrelationID,
		causationID:   metadata.CausationID,
		occurredAt:    metadata.OccurredAt.UTC(),
		version:       ContractVersion,
		data:          data,
	}, nil
}

func (e Envelope[T]) ID() string            { return e.eventID }
func (e Envelope[T]) Type() Type            { return e.eventType }
func (e Envelope[T]) AggregateID() string   { return e.aggregateID }
func (e Envelope[T]) CorrelationID() string { return e.correlationID }
func (e Envelope[T]) CausationID() string   { return e.causationID }
func (e Envelope[T]) OccurredAt() time.Time { return e.occurredAt }
func (e Envelope[T]) Version() int          { return e.version }
func (e Envelope[T]) Data() T               { return e.data }

func (e Envelope[T]) MarshalJSON() ([]byte, error) {
	if e.version != ContractVersion || e.eventType == "" || e.occurredAt.IsZero() {
		return nil, ErrInvalidEvent
	}
	return json.Marshal(struct {
		EventID       string `json:"eventId"`
		EventType     Type   `json:"eventType"`
		AggregateID   string `json:"aggregateId"`
		CorrelationID string `json:"correlationId"`
		CausationID   string `json:"causationId,omitempty"`
		OccurredAt    string `json:"occurredAt"`
		Version       int    `json:"version"`
		Data          T      `json:"data"`
	}{
		EventID:       e.eventID,
		EventType:     e.eventType,
		AggregateID:   e.aggregateID,
		CorrelationID: e.correlationID,
		CausationID:   e.causationID,
		OccurredAt:    e.occurredAt.Format(time.RFC3339Nano),
		Version:       e.version,
		Data:          e.data,
	})
}

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
