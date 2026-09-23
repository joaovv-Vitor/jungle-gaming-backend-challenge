package outbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

type serviceStore struct {
	claims     []Event
	confirmed  int
	retried    int
	retryAfter time.Time
	lastError  string
	confirmErr error
}

func (s *serviceStore) Claim(context.Context, time.Duration) (*Event, error) {
	if len(s.claims) == 0 {
		return nil, nil
	}
	event := s.claims[0]
	s.claims = s.claims[1:]
	return &event, nil
}
func (s *serviceStore) Confirm(context.Context, Event) error {
	s.confirmed++
	if s.confirmErr != nil {
		err := s.confirmErr
		s.confirmErr = nil
		return err
	}
	return nil
}
func (s *serviceStore) Retry(_ context.Context, _ Event, next time.Time, reason string) error {
	s.retried++
	s.retryAfter, s.lastError = next, reason
	return nil
}

func (s *serviceStore) Stats(context.Context) (Stats, error) { return Stats{}, nil }

type servicePublisher struct {
	ids []string
	err error
}

func (*servicePublisher) Check(context.Context) error { return nil }
func (p *servicePublisher) Publish(_ context.Context, event Event) error {
	p.ids = append(p.ids, event.ID)
	return p.err
}

func TestFailedSendReschedulesWithoutConfirming(t *testing.T) {
	const secret = "sensitive-publisher-error-sentinel"
	store := &serviceStore{claims: []Event{{ID: "event-1", GroupID: "wallet-1", Token: "lease-1", Payload: []byte(`{}`), Attempts: 1}}}
	publisher := &servicePublisher{err: errors.New("destination unavailable: " + secret)}
	service := NewService(store, publisher, config.Config{OutboxLease: 30 * time.Second})
	service.jitter = func(time.Duration) time.Duration { return 0 }
	before := time.Now()
	outcome, err := service.ProcessOne(context.Background())
	if outcome != OutcomeRetry || err == nil || store.retried != 1 || store.confirmed != 0 ||
		store.lastError != "publish_unexpected" || strings.Contains(store.lastError, secret) {
		t.Fatalf("outcome=%s error=%v store=%+v", outcome, err, store)
	}
	if store.retryAfter.Before(before.Add(time.Second)) || store.retryAfter.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("retry time = %s", store.retryAfter)
	}
}

func TestUnconfirmedSendCanBeRepublishedWithSameEventID(t *testing.T) {
	store := &serviceStore{
		claims: []Event{
			{ID: "event-1", GroupID: "wallet-1", Token: "lease-1", Payload: []byte(`{}`), Attempts: 1},
			{ID: "event-1", GroupID: "wallet-1", Token: "lease-2", Payload: []byte(`{}`), Attempts: 2},
		},
		confirmErr: errors.New("database unavailable after send"),
	}
	publisher := &servicePublisher{}
	service := NewService(store, publisher, config.Config{OutboxLease: 30 * time.Second})
	if _, err := service.ProcessOne(context.Background()); err == nil {
		t.Fatal("first confirmation unexpectedly succeeded")
	}
	if outcome, err := service.ProcessOne(context.Background()); err != nil || outcome != OutcomeRepublished {
		t.Fatalf("republication = %s, error=%v", outcome, err)
	}
	if len(publisher.ids) != 2 || publisher.ids[0] != "event-1" || publisher.ids[1] != "event-1" || store.confirmed != 2 {
		t.Fatalf("published IDs = %v, confirmations=%d", publisher.ids, store.confirmed)
	}
}

func TestBackoffIsBounded(t *testing.T) {
	zero := func(time.Duration) time.Duration { return 0 }
	if backoff(1, zero) != time.Second || backoff(100, zero) != 5*time.Minute {
		t.Fatal("unexpected outbox retry bounds")
	}
}
