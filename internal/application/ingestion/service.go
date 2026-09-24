package ingestion

import (
	"context"
	"errors"
	"strings"
	"time"

	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
)

var (
	ErrMessageConflict = errors.New("inbox message id reused with another payload")
	ErrInvalidConsumer = errors.New("invalid inbox consumer name")
)

type Claim struct {
	Completed     bool
	TransactionID string
}

type Session interface {
	applicationwagering.Session
	ClaimMessage(context.Context, string, string, PayloadHash, time.Time) (Claim, error)
	CompleteMessage(context.Context, string, string, string, time.Time) error
}

type Store interface {
	WithinTransaction(context.Context, func(Session) error) error
}

type Result struct {
	WagerResult applicationwagering.Result
	Duplicate   bool
}

type Service struct {
	store  Store
	wagers *applicationwagering.Service
	now    func() time.Time
}

func NewService(store Store, wagers *applicationwagering.Service) *Service {
	return &Service{store: store, wagers: wagers, now: time.Now}
}

func (s *Service) Consume(ctx context.Context, consumer string, message Message) (Result, error) {
	if strings.TrimSpace(consumer) != consumer || consumer == "" {
		return Result{}, ErrInvalidConsumer
	}
	if message.ID == "" || message.hash == (PayloadHash{}) {
		return Result{}, ErrInvalidMessage
	}
	var result Result
	process := func(session Session) error {
		claim, err := session.ClaimMessage(ctx, consumer, message.ID, message.hash, s.now().UTC())
		if err != nil {
			return err
		}
		if claim.Completed {
			transaction, err := session.FindByID(ctx, claim.TransactionID)
			if err != nil {
				return err
			}
			if transaction == nil {
				return applicationwagering.ErrTransactionNotFound
			}
			result = Result{
				WagerResult: applicationwagering.Result{Transaction: transaction, Replay: true},
				Duplicate:   true,
			}
			return nil
		}
		wagerResult, err := s.wagers.SubmitInSession(ctx, session, message.Input)
		if err != nil {
			return err
		}
		if err := session.CompleteMessage(ctx, consumer, message.ID, wagerResult.Transaction.ID(), s.now().UTC()); err != nil {
			return err
		}
		result = Result{WagerResult: wagerResult}
		return nil
	}
	err := s.store.WithinTransaction(ctx, process)
	if errors.Is(err, applicationwagering.ErrIdentityRace) {
		err = s.store.WithinTransaction(ctx, process)
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}
