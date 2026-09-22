package wagering

import "errors"

var (
	ErrInvalidTransaction = errors.New("invalid wager transaction")
	ErrInvalidKind        = errors.New("invalid wager transaction kind")
	ErrInvalidStatus      = errors.New("invalid wager transaction status")
	ErrInvalidTransition  = errors.New("invalid wager transaction transition")
	ErrInvalidReference   = errors.New("invalid wager transaction reference")
	ErrInvalidFailureCode = errors.New("invalid failure code")
)

type Origin string

const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

func (o Origin) Valid() bool {
	return o == OriginInternal || o == OriginExternal
}

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

func (k Kind) Valid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	default:
		return false
	}
}

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
		return true
	default:
		return false
	}
}

func (s Status) Terminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

type FailureCode string

const (
	FailureBetInsufficientFunds      FailureCode = "BET_INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureReferenceTypeNotAllowed   FailureCode = "REFERENCE_TYPE_NOT_ALLOWED"
	FailureAlreadyReversed           FailureCode = "ALREADY_REVERSED"
	FailureCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	FailureInvalidOperationAmount    FailureCode = "INVALID_OPERATION_AMOUNT"
	FailureWalletPlayerMismatch      FailureCode = "WALLET_PLAYER_MISMATCH"
	FailureInvalidReference          FailureCode = "INVALID_REFERENCE"
	FailureMoneyOverflow             FailureCode = "MONEY_OVERFLOW"
	FailureInfrastructurePermanent   FailureCode = "INFRASTRUCTURE_PERMANENT_FAILURE"
)

func (c FailureCode) Valid() bool {
	if c == "" || c[0] < 'A' || c[0] > 'Z' {
		return false
	}
	for _, character := range c {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

type PayloadHash [32]byte

func (h PayloadHash) Valid() bool {
	return h != PayloadHash{}
}
