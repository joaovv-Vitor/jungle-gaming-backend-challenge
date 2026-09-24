package wagering

import (
	"context"
	"errors"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/event"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wallet"
)

func (s *Service) process(
	ctx context.Context,
	session Session,
	account *wallet.Wallet,
	transaction *domain.Transaction,
	input SubmitInput,
	now time.Time,
) error {
	reference, pending, failure, err := resolveReference(ctx, session, input)
	if err != nil {
		return err
	}
	if pending {
		return s.persistPending(ctx, session, transaction, input, now)
	}
	return s.finish(ctx, session, account, transaction, input, reference, failure, false, now)
}

// ResumePendingInSession continues a previously persisted transaction. The
// caller owns the wallet lock, pending transaction lock, and SQL transaction.
// A missing or still pending reference remains scheduled until its stored TTL.
func (s *Service) ResumePendingInSession(
	ctx context.Context,
	session Session,
	account *wallet.Wallet,
	transaction *domain.Transaction,
	expired bool,
	now time.Time,
) (bool, error) {
	if transaction == nil || transaction.Status() != domain.StatusPendingReference ||
		account == nil || account.ID() != transaction.WalletID() {
		return false, ErrInvalidInput
	}
	input := SubmitInput{
		ProviderID: transaction.ProviderID(), ExternalTransactionID: transaction.ExternalTransactionID(),
		IdempotencyKey: transaction.IdempotencyKey(), WalletID: transaction.WalletID(),
		PlayerID: transaction.PlayerID(), RoundID: transaction.RoundID(), GameID: transaction.GameID(),
		Kind: transaction.Kind(), Amount: transaction.Amount(),
		ReferenceExternalTransactionID: transaction.ReferenceExternalTransactionID(),
		CorrelationID:                  transaction.ID(),
	}
	if now.Before(transaction.UpdatedAt()) {
		now = transaction.UpdatedAt()
	}
	if now.Before(account.UpdatedAt()) {
		now = account.UpdatedAt()
	}
	reference, pending, failure, err := resolveReference(ctx, session, input)
	if err != nil {
		return false, err
	}
	if pending && !expired {
		return false, nil
	}
	if pending {
		failure = domain.FailureReferenceNotFound
	}
	return true, s.finish(ctx, session, account, transaction, input, reference, failure, true, now)
}

func (s *Service) finish(
	ctx context.Context,
	session Session,
	account *wallet.Wallet,
	transaction *domain.Transaction,
	input SubmitInput,
	reference *domain.Transaction,
	failure domain.FailureCode,
	existing bool,
	now time.Time,
) error {
	if failure != "" {
		return s.persistRejected(ctx, session, transaction, account.Balance(), input, failure, existing, now)
	}
	if reference != nil {
		if err := transaction.ResolveReference(reference.ID(), now); err != nil {
			return err
		}
		if input.Kind == domain.KindRefund || input.Kind == domain.KindRollback {
			reversed, err := session.HasProcessedReversal(ctx, reference.ID())
			if err != nil {
				return err
			}
			if reversed {
				return s.persistRejected(ctx, session, transaction, account.Balance(), input, domain.FailureAlreadyReversed, existing, now)
			}
		}
	}

	if input.Kind == domain.KindLoss {
		if err := transaction.MarkProcessed(account.Balance(), now); err != nil {
			return err
		}
		if err := persistTransaction(ctx, session, transaction, existing); err != nil {
			return err
		}
		_, err := s.insertProcessedEvent(ctx, session, transaction, input, account.Balance(), now)
		return err
	}

	direction := ledger.DirectionCredit
	if input.Kind == domain.KindBet || (input.Kind == domain.KindRollback && reference.Kind() != domain.KindBet) {
		direction = ledger.DirectionDebit
	}
	var before, after money.Money
	var err error
	if direction == ledger.DirectionDebit {
		before, after, err = account.Debit(input.Amount, now)
	} else {
		before, after, err = account.Credit(input.Amount, now)
	}
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		code := domain.FailureBetInsufficientFunds
		if input.Kind == domain.KindRollback {
			code = domain.FailureReversalInsufficientFunds
		}
		return s.persistRejected(ctx, session, transaction, account.Balance(), input, code, existing, now)
	}
	if errors.Is(err, money.ErrOverflow) {
		return s.persistRejected(ctx, session, transaction, account.Balance(), input, domain.FailureMoneyOverflow, existing, now)
	}
	if err != nil {
		return err
	}
	if err := transaction.MarkProcessed(after, now); err != nil {
		return err
	}
	entryID, err := s.newID()
	if err != nil {
		return err
	}
	entry, err := ledger.New(ledger.Params{
		ID: entryID, WalletID: account.ID(), TransactionID: transaction.ID(), Direction: direction,
		Amount: input.Amount, BalanceBefore: before, BalanceAfter: after,
		WalletVersion: account.Version(), CreatedAt: now,
	})
	if err != nil {
		return err
	}
	if err := session.UpdateWallet(ctx, account); err != nil {
		return err
	}
	if err := persistTransaction(ctx, session, transaction, existing); err != nil {
		return err
	}
	if err := session.InsertLedger(ctx, entry); err != nil {
		return err
	}
	processedEventID, err := s.insertProcessedEvent(ctx, session, transaction, input, after, now)
	if err != nil {
		return err
	}
	return s.insertBalanceEvent(ctx, session, transaction, input, direction, before, after, account.Version(), processedEventID, now)
}

func persistTransaction(ctx context.Context, session Session, transaction *domain.Transaction, existing bool) error {
	if existing {
		return session.UpdateTransaction(ctx, transaction)
	}
	return session.InsertTransaction(ctx, transaction, nil)
}

func resolveReference(
	ctx context.Context,
	session Session,
	input SubmitInput,
) (*domain.Transaction, bool, domain.FailureCode, error) {
	if input.ReferenceExternalTransactionID == "" {
		return nil, false, "", nil
	}
	reference, err := session.FindByExternalID(ctx, input.ProviderID, input.ReferenceExternalTransactionID)
	if err != nil {
		return nil, false, "", err
	}
	if reference == nil || reference.Status() == domain.StatusPending || reference.Status() == domain.StatusPendingReference {
		return nil, true, "", nil
	}
	if reference.Status() != domain.StatusProcessed {
		return reference, false, domain.FailureReferenceNotProcessed, nil
	}
	if reference.ProviderID() != input.ProviderID || reference.PlayerID() != input.PlayerID ||
		reference.WalletID() != input.WalletID || reference.RoundID() != input.RoundID ||
		reference.Amount().Currency() != input.Amount.Currency() {
		return reference, false, domain.FailureReferenceMismatch, nil
	}
	if input.Kind == domain.KindRefund && reference.Kind() != domain.KindBet {
		return reference, false, domain.FailureReferenceTypeNotAllowed, nil
	}
	if input.Kind == domain.KindRollback && reference.Kind() != domain.KindBet &&
		reference.Kind() != domain.KindWin && reference.Kind() != domain.KindRefund {
		return reference, false, domain.FailureReferenceTypeNotAllowed, nil
	}
	if input.Kind == domain.KindWin && reference.Kind() != domain.KindBet {
		return reference, false, domain.FailureReferenceTypeNotAllowed, nil
	}
	if (input.Kind == domain.KindRefund || input.Kind == domain.KindRollback) && !reference.Amount().Equal(input.Amount) {
		return reference, false, domain.FailureInvalidOperationAmount, nil
	}
	return reference, false, "", nil
}

func (s *Service) persistPending(
	ctx context.Context,
	session Session,
	transaction *domain.Transaction,
	input SubmitInput,
	now time.Time,
) error {
	if err := transaction.MarkPendingReference(now); err != nil {
		return err
	}
	schedule := &ReferenceSchedule{NextAttemptAt: now.Add(time.Second), ExpiresAt: now.Add(24 * time.Hour)}
	if err := session.InsertTransaction(ctx, transaction, schedule); err != nil {
		return err
	}
	eventID, err := s.newID()
	if err != nil {
		return err
	}
	pendingEvent, err := event.NewWagerTransactionPendingReference(event.Metadata{
		EventID: eventID, AggregateID: transaction.ID(), CorrelationID: input.CorrelationID, OccurredAt: now,
	}, event.PendingReferenceInput{
		TransactionID: transaction.ID(), ProviderID: input.ProviderID, Kind: input.Kind,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
	})
	if err != nil {
		return err
	}
	return session.InsertEvent(ctx, pendingEvent)
}

func (s *Service) persistRejected(
	ctx context.Context,
	session Session,
	transaction *domain.Transaction,
	balance money.Money,
	input SubmitInput,
	code domain.FailureCode,
	existing bool,
	now time.Time,
) error {
	if err := transaction.Reject(code, balance, now); err != nil {
		return err
	}
	if err := persistTransaction(ctx, session, transaction, existing); err != nil {
		return err
	}
	eventID, err := s.newID()
	if err != nil {
		return err
	}
	rejectedEvent, err := event.NewWagerTransactionRejected(event.Metadata{
		EventID: eventID, AggregateID: transaction.ID(), CorrelationID: input.CorrelationID, OccurredAt: now,
	}, event.RejectedInput{
		TransactionID: transaction.ID(), ProviderID: input.ProviderID, Kind: input.Kind,
		FailureCode: code, Money: input.Amount, Balance: balance,
	})
	if err != nil {
		return err
	}
	return session.InsertEvent(ctx, rejectedEvent)
}

func (s *Service) insertProcessedEvent(
	ctx context.Context,
	session Session,
	transaction *domain.Transaction,
	input SubmitInput,
	balance money.Money,
	now time.Time,
) (string, error) {
	eventID, err := s.newID()
	if err != nil {
		return "", err
	}
	processedEvent, err := event.NewWagerTransactionProcessed(event.Metadata{
		EventID: eventID, AggregateID: transaction.ID(), CorrelationID: input.CorrelationID, OccurredAt: now,
	}, event.ProcessedInput{
		TransactionID: transaction.ID(), ProviderID: input.ProviderID, Kind: input.Kind,
		Money: input.Amount, Balance: balance,
	})
	if err != nil {
		return "", err
	}
	if err := session.InsertEvent(ctx, processedEvent); err != nil {
		return "", err
	}
	return eventID, nil
}

func (s *Service) insertBalanceEvent(
	ctx context.Context,
	session Session,
	transaction *domain.Transaction,
	input SubmitInput,
	direction ledger.Direction,
	before, after money.Money,
	walletVersion int64,
	causationID string,
	now time.Time,
) error {
	eventID, err := s.newID()
	if err != nil {
		return err
	}
	balanceEvent, err := event.NewWalletBalanceChanged(event.Metadata{
		EventID: eventID, AggregateID: input.WalletID, CorrelationID: input.CorrelationID,
		CausationID: causationID, OccurredAt: now,
	}, event.WalletBalanceChangedData{
		WalletID: input.WalletID, TransactionID: transaction.ID(), Direction: direction,
		Money: input.Amount, BalanceBefore: before, BalanceAfter: after,
		WalletVersion: walletVersion,
	})
	if err != nil {
		return err
	}
	return session.InsertEvent(ctx, balanceEvent)
}
