package outbox

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/safeerror"
)

var ErrInvalidClaim = errors.New("invalid outbox claim")

type Event struct {
	ID            string
	GroupID       string
	CorrelationID string
	Payload       []byte
	Token         string
	Attempts      int
}

type Store interface {
	Claim(context.Context, time.Duration) (*Event, error)
	Confirm(context.Context, Event) error
	Retry(context.Context, Event, time.Time, string) error
	Stats(context.Context) (Stats, error)
}

type Stats struct {
	Pending          int64
	OldestAgeSeconds float64
}

type Publisher interface {
	Check(context.Context) error
	Publish(context.Context, Event) error
}

type Outcome string

const (
	OutcomeIdle        Outcome = "idle"
	OutcomePublished   Outcome = "published"
	OutcomeRepublished Outcome = "republished"
	OutcomeRetry       Outcome = "retry"
)

type Service struct {
	store     Store
	publisher Publisher
	lease     time.Duration
	jitter    func(time.Duration) time.Duration
	logger    *slog.Logger
}

func NewLoggedService(store Store, publisher Publisher, cfg config.Config, logger *slog.Logger) *Service {
	service := NewService(store, publisher, cfg)
	service.logger = logger
	return service
}

func NewService(store Store, publisher Publisher, cfg config.Config) *Service {
	return &Service{
		store: store, publisher: publisher, lease: cfg.OutboxLease,
		jitter: func(max time.Duration) time.Duration { return time.Duration(rand.Int63n(int64(max))) },
	}
}

func (s *Service) CheckDestination(ctx context.Context) error { return s.publisher.Check(ctx) }
func (s *Service) Stats(ctx context.Context) (Stats, error)   { return s.store.Stats(ctx) }

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
		if retryErr := s.store.Retry(retryCtx, *event, next, "publish_"+safeerror.Reason(err)); retryErr != nil {
			return OutcomeRetry, errors.Join(err, retryErr)
		}
		return OutcomeRetry, err
	}
	if err := s.store.Confirm(ctx, *event); err != nil {
		return "", err
	}
	if s.logger != nil {
		s.logger.Info("outbox event confirmed", "eventId", event.ID, "correlationId", event.CorrelationID,
			"attempts", event.Attempts)
	}
	if event.Attempts > 1 {
		return OutcomeRepublished, nil
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
