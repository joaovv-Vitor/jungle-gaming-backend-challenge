package wagering

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/event"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wallet"
	platformid "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/id"
)

var (
	ErrInvalidInput        = errors.New("invalid wagering input")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrWalletMismatch      = errors.New("wallet does not belong to player or currency")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different payload")
	ErrExternalIDConflict  = errors.New("external transaction id reused with another idempotency key")
	ErrIdentityRace        = errors.New("wager identity was inserted concurrently")
	ErrConcurrentWrite     = errors.New("concurrent wallet write")
	ErrTransactionNotFound = errors.New("wager transaction not found")
)

type SubmitInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	WalletID                       string
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           domain.Kind
	Amount                         money.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
}

type Result struct {
	Transaction *domain.Transaction
	Replay      bool
}

type ReferenceSchedule struct {
	NextAttemptAt time.Time
	ExpiresAt     time.Time
}

type Store interface {
	WithinTransaction(context.Context, func(Session) error) error
}

type Session interface {
	FindByID(context.Context, string) (*domain.Transaction, error)
	FindByIdempotencyKey(context.Context, string, string) (*domain.Transaction, error)
	FindByExternalID(context.Context, string, string) (*domain.Transaction, error)
	LockWallet(context.Context, string) (*wallet.Wallet, error)
	HasProcessedReversal(context.Context, string) (bool, error)
	InsertTransaction(context.Context, *domain.Transaction, *ReferenceSchedule) error
	UpdateTransaction(context.Context, *domain.Transaction) error
	UpdateWallet(context.Context, *wallet.Wallet) error
	InsertLedger(context.Context, *ledger.Entry) error
	InsertEvent(context.Context, event.IntegrationEvent) error
}

func (s *Service) FindByID(ctx context.Context, providerID, transactionID string) (*domain.Transaction, error) {
	if providerID == "" || platformid.Validate(transactionID) != nil {
		return nil, ErrInvalidInput
	}
	var found *domain.Transaction
	err := s.store.WithinTransaction(ctx, func(session Session) error {
		transaction, err := session.FindByID(ctx, transactionID)
		if err != nil {
			return err
		}
		if transaction == nil || transaction.ProviderID() != providerID {
			return ErrTransactionNotFound
		}
		found = transaction
		return nil
	})
	return found, err
}

func (s *Service) FindByExternalID(ctx context.Context, providerID, externalID string) (*domain.Transaction, error) {
	if providerID == "" || externalID == "" || strings.TrimSpace(externalID) != externalID {
		return nil, ErrInvalidInput
	}
	var found *domain.Transaction
	err := s.store.WithinTransaction(ctx, func(session Session) error {
		transaction, err := session.FindByExternalID(ctx, providerID, externalID)
		if err != nil {
			return err
		}
		if transaction == nil {
			return ErrTransactionNotFound
		}
		found = transaction
		return nil
	})
	return found, err
}

type Service struct {
	store Store
	now   func() time.Time
	newID func() (string, error)
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: platformid.New}
}

func (s *Service) Submit(ctx context.Context, input SubmitInput) (Result, error) {
	var result Result
	process := func(session Session) error {
		var err error
		result, err = s.SubmitInSession(ctx, session, input)
		return err
	}
	err := s.store.WithinTransaction(ctx, process)
	if errors.Is(err, ErrIdentityRace) {
		err = s.store.WithinTransaction(ctx, process)
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// SubmitInSession processes an operation inside a transaction owned by the
// caller. It allows transports with their own durable state, such as an SQS
// inbox, to commit that state atomically with the financial effects.
func (s *Service) SubmitInSession(ctx context.Context, session Session, input SubmitInput) (Result, error) {
	if err := validateInput(input); err != nil {
		return Result{}, err
	}
	_, payloadHash, err := canonicalPayload(input)
	if err != nil {
		return Result{}, fmt.Errorf("canonical payload: %w", err)
	}
	transactionID, err := s.newID()
	if err != nil {
		return Result{}, err
	}
	receivedAt := s.now().UTC()
	transaction, err := domain.NewExternal(domain.ExternalParams{
		ID: transactionID, ProviderID: input.ProviderID,
		ExternalTransactionID: input.ExternalTransactionID, IdempotencyKey: input.IdempotencyKey,
		PayloadHash: payloadHash, WalletID: input.WalletID, PlayerID: input.PlayerID,
		RoundID: input.RoundID, GameID: input.GameID, Kind: input.Kind, Amount: input.Amount,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
	}, receivedAt)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	replay, handled, err := evaluateIdentities(ctx, session, input, payloadHash)
	if err != nil {
		return Result{}, err
	}
	if handled {
		return Result{Transaction: replay, Replay: true}, nil
	}
	account, err := session.LockWallet(ctx, input.WalletID)
	if err != nil {
		return Result{}, err
	}
	if account.PlayerID() != input.PlayerID || account.Currency() != input.Amount.Currency() {
		return Result{}, ErrWalletMismatch
	}
	replay, handled, err = evaluateIdentities(ctx, session, input, payloadHash)
	if err != nil {
		return Result{}, err
	}
	if handled {
		return Result{Transaction: replay, Replay: true}, nil
	}
	processingTime := s.now().UTC()
	if processingTime.Before(receivedAt) {
		processingTime = receivedAt
	}
	if processingTime.Before(account.UpdatedAt()) {
		processingTime = account.UpdatedAt()
	}
	if err := s.process(ctx, session, account, transaction, input, processingTime); err != nil {
		return Result{}, err
	}
	return Result{Transaction: transaction}, nil
}

func evaluateIdentities(
	ctx context.Context,
	session Session,
	input SubmitInput,
	hash domain.PayloadHash,
) (*domain.Transaction, bool, error) {
	byKey, err := session.FindByIdempotencyKey(ctx, input.ProviderID, input.IdempotencyKey)
	if err != nil {
		return nil, false, err
	}
	byExternal, err := session.FindByExternalID(ctx, input.ProviderID, input.ExternalTransactionID)
	if err != nil {
		return nil, false, err
	}
	if byKey != nil {
		if byKey.PayloadHash() != hash {
			return nil, false, ErrIdempotencyConflict
		}
		if byExternal == nil || byExternal.ID() != byKey.ID() {
			return nil, false, ErrExternalIDConflict
		}
		return byKey, true, nil
	}
	if byExternal != nil {
		// Under READ COMMITTED each SELECT gets a fresh snapshot. A concurrent
		// insert can therefore become visible between the idempotency and
		// external-ID lookups. Re-evaluate the complete identity from the row
		// that is visible now instead of reporting a false external-ID conflict.
		if byExternal.IdempotencyKey() == input.IdempotencyKey {
			if byExternal.PayloadHash() != hash {
				return nil, false, ErrIdempotencyConflict
			}
			return byExternal, true, nil
		}
		return nil, false, ErrExternalIDConflict
	}
	return nil, false, nil
}

func validateInput(input SubmitInput) error {
	if strings.TrimSpace(input.ProviderID) != input.ProviderID || input.ProviderID == "" ||
		strings.TrimSpace(input.ExternalTransactionID) != input.ExternalTransactionID || input.ExternalTransactionID == "" ||
		strings.TrimSpace(input.IdempotencyKey) != input.IdempotencyKey || input.IdempotencyKey == "" ||
		strings.TrimSpace(input.RoundID) != input.RoundID || input.RoundID == "" ||
		strings.TrimSpace(input.GameID) != input.GameID || input.GameID == "" || input.CorrelationID == "" {
		return ErrInvalidInput
	}
	if platformid.Validate(input.WalletID) != nil || platformid.Validate(input.PlayerID) != nil {
		return ErrInvalidInput
	}
	if err := input.Amount.Validate(); err != nil || input.Amount.IsNegative() {
		return ErrInvalidInput
	}
	return nil
}
