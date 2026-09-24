package wagering

import (
	"fmt"
	"strings"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
)

type Transaction struct {
	id                             string
	origin                         Origin
	providerID                     string
	externalTransactionID          string
	idempotencyKey                 string
	payloadHash                    PayloadHash
	walletID                       string
	playerID                       string
	roundID                        string
	gameID                         string
	kind                           Kind
	amount                         money.Money
	referenceExternalTransactionID string
	referenceTransactionID         string
	status                         Status
	failureCode                    FailureCode
	resultBalance                  *money.Money
	createdAt                      time.Time
	updatedAt                      time.Time
}

type ExternalParams struct {
	ID                             string
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    PayloadHash
	WalletID                       string
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Amount                         money.Money
	ReferenceExternalTransactionID string
}

type Rehydration struct {
	ID                             string
	Origin                         Origin
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    PayloadHash
	WalletID                       string
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Amount                         money.Money
	ReferenceExternalTransactionID string
	ReferenceTransactionID         string
	Status                         Status
	FailureCode                    FailureCode
	ResultBalance                  *money.Money
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

func NewExternal(params ExternalParams, now time.Time) (*Transaction, error) {
	state := Rehydration{
		ID:                             params.ID,
		Origin:                         OriginExternal,
		ProviderID:                     params.ProviderID,
		ExternalTransactionID:          params.ExternalTransactionID,
		IdempotencyKey:                 params.IdempotencyKey,
		PayloadHash:                    params.PayloadHash,
		WalletID:                       params.WalletID,
		PlayerID:                       params.PlayerID,
		RoundID:                        params.RoundID,
		GameID:                         params.GameID,
		Kind:                           params.Kind,
		Amount:                         params.Amount,
		ReferenceExternalTransactionID: params.ReferenceExternalTransactionID,
		Status:                         StatusPending,
		CreatedAt:                      now,
		UpdatedAt:                      now,
	}
	return build(state)
}

func NewOpening(id, walletID, playerID string, amount money.Money, now time.Time) (*Transaction, error) {
	result := amount
	return build(Rehydration{
		ID:            id,
		Origin:        OriginInternal,
		WalletID:      walletID,
		PlayerID:      playerID,
		Kind:          KindOpening,
		Amount:        amount,
		Status:        StatusProcessed,
		ResultBalance: &result,
		CreatedAt:     now,
		UpdatedAt:     now,
	})
}

func Rehydrate(state Rehydration) (*Transaction, error) {
	return build(state)
}

func build(state Rehydration) (*Transaction, error) {
	if !validID(state.ID) || !validID(state.WalletID) || !validID(state.PlayerID) {
		return nil, fmt.Errorf("%w: transaction, wallet and player ids are required", ErrInvalidTransaction)
	}
	if !state.Origin.Valid() {
		return nil, fmt.Errorf("%w: origin", ErrInvalidTransaction)
	}
	if !state.Kind.Valid() {
		return nil, ErrInvalidKind
	}
	if !state.Status.Valid() {
		return nil, ErrInvalidStatus
	}
	if err := state.Amount.Validate(); err != nil {
		return nil, fmt.Errorf("%w: amount: %v", ErrInvalidTransaction, err)
	}
	if state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() || state.UpdatedAt.Before(state.CreatedAt) {
		return nil, fmt.Errorf("%w: timestamps", ErrInvalidTransaction)
	}
	if err := validateOrigin(state); err != nil {
		return nil, err
	}
	if err := validateKindAndReference(state); err != nil {
		return nil, err
	}
	if err := validateResult(state); err != nil {
		return nil, err
	}

	transaction := &Transaction{
		id:                             state.ID,
		origin:                         state.Origin,
		providerID:                     state.ProviderID,
		externalTransactionID:          state.ExternalTransactionID,
		idempotencyKey:                 state.IdempotencyKey,
		payloadHash:                    state.PayloadHash,
		walletID:                       state.WalletID,
		playerID:                       state.PlayerID,
		roundID:                        state.RoundID,
		gameID:                         state.GameID,
		kind:                           state.Kind,
		amount:                         state.Amount,
		referenceExternalTransactionID: state.ReferenceExternalTransactionID,
		referenceTransactionID:         state.ReferenceTransactionID,
		status:                         state.Status,
		failureCode:                    state.FailureCode,
		createdAt:                      state.CreatedAt.UTC(),
		updatedAt:                      state.UpdatedAt.UTC(),
	}
	if state.ResultBalance != nil {
		result := *state.ResultBalance
		transaction.resultBalance = &result
	}
	return transaction, nil
}

func validateOrigin(state Rehydration) error {
	if state.Origin == OriginInternal {
		if state.Kind != KindOpening || state.Status != StatusProcessed || !state.Amount.IsPositive() {
			return fmt.Errorf("%w: internal origin must be a processed positive opening", ErrInvalidTransaction)
		}
		if state.ProviderID != "" || state.ExternalTransactionID != "" || state.IdempotencyKey != "" ||
			state.PayloadHash.Valid() || state.RoundID != "" || state.GameID != "" ||
			state.ReferenceExternalTransactionID != "" || state.ReferenceTransactionID != "" {
			return fmt.Errorf("%w: internal opening contains external metadata", ErrInvalidTransaction)
		}
		return nil
	}

	if state.Kind == KindOpening {
		return fmt.Errorf("%w: opening cannot be external", ErrInvalidKind)
	}
	if !validID(state.ProviderID) || !validID(state.ExternalTransactionID) || !validID(state.IdempotencyKey) ||
		!validID(state.RoundID) || !validID(state.GameID) || !state.PayloadHash.Valid() {
		return fmt.Errorf("%w: external metadata is incomplete", ErrInvalidTransaction)
	}
	return nil
}

func validateKindAndReference(state Rehydration) error {
	if state.Kind == KindLoss {
		if !state.Amount.IsZero() {
			return fmt.Errorf("%w: LOSS requires zero", ErrInvalidTransaction)
		}
	} else if !state.Amount.IsPositive() {
		return fmt.Errorf("%w: %s requires a positive amount", ErrInvalidTransaction, state.Kind)
	}

	reference := state.ReferenceExternalTransactionID
	switch state.Kind {
	case KindRefund, KindRollback:
		if !validID(reference) {
			return fmt.Errorf("%w: %s requires a reference", ErrInvalidReference, state.Kind)
		}
	case KindWin:
		if reference != "" && !validID(reference) {
			return ErrInvalidReference
		}
	case KindBet, KindLoss, KindOpening:
		if reference != "" {
			return fmt.Errorf("%w: %s does not accept a reference", ErrInvalidReference, state.Kind)
		}
	}
	if reference != "" && reference == state.ExternalTransactionID {
		return fmt.Errorf("%w: self reference", ErrInvalidReference)
	}
	if state.ReferenceTransactionID != "" && (reference == "" || !validID(state.ReferenceTransactionID)) {
		return ErrInvalidReference
	}
	if state.Status == StatusPendingReference && reference == "" {
		return fmt.Errorf("%w: pending reference without external reference", ErrInvalidTransaction)
	}
	return nil
}

func validateResult(state Rehydration) error {
	switch state.Status {
	case StatusPending, StatusPendingReference:
		if state.FailureCode != "" || state.ResultBalance != nil {
			return fmt.Errorf("%w: pending transaction has a terminal result", ErrInvalidTransaction)
		}
	case StatusProcessed:
		if state.FailureCode != "" || state.ResultBalance == nil {
			return fmt.Errorf("%w: processed transaction result", ErrInvalidTransaction)
		}
	case StatusRejected:
		if !state.FailureCode.Valid() || state.ResultBalance == nil {
			return fmt.Errorf("%w: rejected transaction result", ErrInvalidTransaction)
		}
	case StatusFailed:
		if !state.FailureCode.Valid() || state.ResultBalance != nil {
			return fmt.Errorf("%w: failed transaction result", ErrInvalidTransaction)
		}
	}
	if state.ResultBalance != nil {
		if err := state.ResultBalance.Validate(); err != nil || state.ResultBalance.IsNegative() {
			return fmt.Errorf("%w: invalid result balance", ErrInvalidTransaction)
		}
		if state.ResultBalance.Currency() != state.Amount.Currency() {
			return fmt.Errorf("%w: result balance", money.ErrCurrencyMismatch)
		}
		if state.Origin == OriginInternal && !state.ResultBalance.Equal(state.Amount) {
			return fmt.Errorf("%w: opening result must equal its amount", ErrInvalidTransaction)
		}
	}
	if state.Status == StatusProcessed && state.ReferenceExternalTransactionID != "" && state.ReferenceTransactionID == "" {
		return fmt.Errorf("%w: processed reference was not resolved", ErrInvalidReference)
	}
	return nil
}

func (t *Transaction) MarkPendingReference(now time.Time) error {
	if t == nil || t.status != StatusPending || t.referenceExternalTransactionID == "" {
		return ErrInvalidTransition
	}
	if err := t.validateTransitionTime(now); err != nil {
		return err
	}
	t.status = StatusPendingReference
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) ResolveReference(transactionID string, now time.Time) error {
	if t == nil || (t.status != StatusPending && t.status != StatusPendingReference) {
		return ErrInvalidTransition
	}
	if t.referenceExternalTransactionID == "" || !validID(transactionID) {
		return ErrInvalidReference
	}
	if t.referenceTransactionID != "" && t.referenceTransactionID != transactionID {
		return ErrInvalidReference
	}
	if err := t.validateTransitionTime(now); err != nil {
		return err
	}
	t.referenceTransactionID = transactionID
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) MarkProcessed(balance money.Money, now time.Time) error {
	if t != nil && t.referenceExternalTransactionID != "" && t.referenceTransactionID == "" {
		return ErrInvalidReference
	}
	if err := t.prepareTerminal(balance, now); err != nil {
		return err
	}
	t.status = StatusProcessed
	t.failureCode = ""
	t.resultBalance = &balance
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) Reject(code FailureCode, balance money.Money, now time.Time) error {
	if !code.Valid() {
		return ErrInvalidFailureCode
	}
	if err := t.prepareTerminal(balance, now); err != nil {
		return err
	}
	t.status = StatusRejected
	t.failureCode = code
	t.resultBalance = &balance
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) Fail(code FailureCode, now time.Time) error {
	if t == nil || t.status.Terminal() {
		return ErrInvalidTransition
	}
	if !code.Valid() || code != FailureInfrastructurePermanent {
		return ErrInvalidFailureCode
	}
	if err := t.validateTransitionTime(now); err != nil {
		return err
	}
	t.status = StatusFailed
	t.failureCode = code
	t.resultBalance = nil
	t.updatedAt = now.UTC()
	return nil
}

func (t *Transaction) prepareTerminal(balance money.Money, now time.Time) error {
	if t == nil || t.status.Terminal() {
		return ErrInvalidTransition
	}
	if err := t.validateTransitionTime(now); err != nil {
		return err
	}
	if err := balance.Validate(); err != nil || balance.IsNegative() {
		return fmt.Errorf("%w: result balance", ErrInvalidTransaction)
	}
	if balance.Currency() != t.amount.Currency() {
		return money.ErrCurrencyMismatch
	}
	return nil
}

func (t *Transaction) validateTransitionTime(now time.Time) error {
	if now.IsZero() || now.Before(t.updatedAt) {
		return fmt.Errorf("%w: transition time", ErrInvalidTransaction)
	}
	return nil
}

func (t *Transaction) ID() string                    { return t.id }
func (t *Transaction) Origin() Origin                { return t.origin }
func (t *Transaction) ProviderID() string            { return t.providerID }
func (t *Transaction) ExternalTransactionID() string { return t.externalTransactionID }
func (t *Transaction) IdempotencyKey() string        { return t.idempotencyKey }
func (t *Transaction) PayloadHash() PayloadHash      { return t.payloadHash }
func (t *Transaction) WalletID() string              { return t.walletID }
func (t *Transaction) PlayerID() string              { return t.playerID }
func (t *Transaction) RoundID() string               { return t.roundID }
func (t *Transaction) GameID() string                { return t.gameID }
func (t *Transaction) Kind() Kind                    { return t.kind }
func (t *Transaction) Amount() money.Money           { return t.amount }
func (t *Transaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}
func (t *Transaction) ReferenceTransactionID() string { return t.referenceTransactionID }
func (t *Transaction) Status() Status                 { return t.status }
func (t *Transaction) FailureCode() FailureCode       { return t.failureCode }
func (t *Transaction) CreatedAt() time.Time           { return t.createdAt }
func (t *Transaction) UpdatedAt() time.Time           { return t.updatedAt }

func (t *Transaction) ResultBalance() (money.Money, bool) {
	if t.resultBalance == nil {
		return money.Money{}, false
	}
	return *t.resultBalance, true
}

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
