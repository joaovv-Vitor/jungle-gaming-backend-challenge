package reference

import (
	"context"
	"errors"
	"math/rand"
	"time"

	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
)

var ErrInvalidClaim = errors.New("invalid pending reference claim")

type Claim struct {
	TransactionID string
	WalletID      string
	Token         string
}

type Pending struct {
	Transaction *domain.Transaction
	Attempts    int
	ExpiresAt   time.Time
	Now         time.Time
}

type Session interface {
	applicationwagering.Session
	LockPending(context.Context, Claim) (*Pending, error)
	Reschedule(context.Context, Claim, time.Time) error
}

type Store interface {
	Claim(context.Context, time.Duration) (*Claim, error)
	WithinTransaction(context.Context, func(Session) error) error
}

type Outcome string

const (
	OutcomeIdle        Outcome = "idle"
	OutcomeStale       Outcome = "stale"
	OutcomeRescheduled Outcome = "rescheduled"
	OutcomeCompleted   Outcome = "completed"
)

type Service struct {
	store  Store
	wagers *applicationwagering.Service
	lease  time.Duration
	jitter func(time.Duration) time.Duration
}

func NewService(store Store, wagers *applicationwagering.Service, cfg config.Config) *Service {
	return &Service{
		store: store, wagers: wagers,
		lease:  cfg.ReferenceLease,
		jitter: func(max time.Duration) time.Duration { return time.Duration(rand.Int63n(int64(max))) },
	}
}

// ProcessOne claims at most one due item. A claim is committed before the
// financial transaction takes the wallet lock.
func (s *Service) ProcessOne(ctx context.Context) (Outcome, error) {
	outcome, _, err := s.ProcessOneWithClaim(ctx)
	return outcome, err
}

// ProcessOneWithClaim exposes only the claimed identifiers for operational logs.
func (s *Service) ProcessOneWithClaim(ctx context.Context) (Outcome, *Claim, error) {
	claim, err := s.store.Claim(ctx, s.lease)
	if err != nil {
		return "", nil, err
	}
	if claim == nil {
		return OutcomeIdle, nil, nil
	}
	if claim.TransactionID == "" || claim.WalletID == "" || claim.Token == "" {
		return "", claim, ErrInvalidClaim
	}
	outcome := OutcomeStale
	err = s.store.WithinTransaction(ctx, func(session Session) error {
		outcome = OutcomeStale
		account, err := session.LockWallet(ctx, claim.WalletID)
		if err != nil {
			return err
		}
		pending, err := session.LockPending(ctx, *claim)
		if err != nil || pending == nil {
			return err
		}
		if pending.Transaction == nil || pending.ExpiresAt.IsZero() || pending.Now.IsZero() {
			return ErrInvalidClaim
		}
		completed, err := s.wagers.ResumePendingInSession(
			ctx, session, account, pending.Transaction, !pending.Now.Before(pending.ExpiresAt), pending.Now,
		)
		if err != nil {
			return err
		}
		if completed {
			outcome = OutcomeCompleted
			return nil
		}
		next := pending.Now.Add(backoff(pending.Attempts+1, s.jitter))
		if next.After(pending.ExpiresAt) {
			next = pending.ExpiresAt
		}
		if err := session.Reschedule(ctx, *claim, next); err != nil {
			return err
		}
		outcome = OutcomeRescheduled
		return nil
	})
	if err != nil {
		return "", claim, err
	}
	return outcome, claim, nil
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
