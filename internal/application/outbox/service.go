package outbox

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

var ErrInvalidClaim = errors.New("invalid outbox claim")

type Event struct {
	ID       string
	GroupID  string
	Payload  []byte
	Token    string
	Attempts int
}

type Store interface {
	Claim(context.Context, time.Duration) (*Event, error)
	Confirm(context.Context, Event) error
	Retry(context.Context, Event, time.Time, string) error
}

type Publisher interface {
	Check(context.Context) error
	Publish(context.Context, Event) error
}

type Outcome string

const (
	OutcomeIdle      Outcome = "idle"
	OutcomePublished Outcome = "published"
	OutcomeRetry     Outcome = "retry"
)

type Service struct {
	store     Store
	publisher Publisher
	lease     time.Duration
	jitter    func(time.Duration) time.Duration
}

func NewService(store Store, publisher Publisher, cfg config.Config) *Service {
	return &Service{
		store: store, publisher: publisher, lease: cfg.OutboxLease,
		jitter: func(max time.Duration) time.Duration { return time.Duration(rand.Int63n(int64(max))) },
	}
}

func (s *Service) CheckDestination(ctx context.Context) error { return s.publisher.Check(ctx) }

// ProcessOne publishes only after a separately committed claim. A crash after
// SendMessage and before Confirm may redeliver the same event ID.
func (s *Service) ProcessOne(ctx context.Context) (Outcome, error) {
	event, err := s.store.Claim(ctx, s.lease)
	if err != nil {
		return "", err
	}
	if event == nil {
		return OutcomeIdle, nil
	}
	if event.ID == "" || event.GroupID == "" || event.Token == "" || len(event.Payload) == 0 || event.Attempts < 1 {
		return "", ErrInvalidClaim
	}
	if err := s.publisher.Publish(ctx, *event); err != nil {
		next := time.Now().UTC().Add(backoff(event.Attempts, s.jitter))
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if retryErr := s.store.Retry(retryCtx, *event, next, truncate(err.Error(), 512)); retryErr != nil {
			return OutcomeRetry, errors.Join(err, retryErr)
		}
		return OutcomeRetry, err
	}
	if err := s.store.Confirm(ctx, *event); err != nil {
		return "", err
	}
	return OutcomePublished, nil
}

func backoff(attempt int, jitter func(time.Duration) time.Duration) time.Duration {
	delay := time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
	if delay == 5*time.Minute {
		return delay
	}
	return delay + jitter(delay/4+1)
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
