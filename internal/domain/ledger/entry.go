package ledger

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
)

var (
	ErrInvalidEntry     = errors.New("invalid wallet ledger entry")
	ErrInvalidDirection = errors.New("invalid ledger direction")
	ErrBalanceEquation  = errors.New("invalid ledger balance equation")
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

func (d Direction) Valid() bool {
	return d == DirectionDebit || d == DirectionCredit
}

type Entry struct {
	id            string
	walletID      string
	transactionID string
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	walletVersion int64
	createdAt     time.Time
}

type Params struct {
	ID            string
	WalletID      string
	TransactionID string
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
	CreatedAt     time.Time
}

func New(params Params) (*Entry, error) {
	return build(params)
}

func Rehydrate(params Params) (*Entry, error) {
	return build(params)
}

func build(params Params) (*Entry, error) {
	if !validID(params.ID) || !validID(params.WalletID) || !validID(params.TransactionID) {
		return nil, fmt.Errorf("%w: ids are required", ErrInvalidEntry)
	}
	if !params.Direction.Valid() {
		return nil, ErrInvalidDirection
	}
	if !params.Amount.IsPositive() {
		if err := params.Amount.Validate(); err != nil {
			return nil, fmt.Errorf("%w: amount: %v", ErrInvalidEntry, err)
		}
		return nil, fmt.Errorf("%w: amount must be positive", ErrInvalidEntry)
	}
	if err := params.BalanceBefore.Validate(); err != nil {
		return nil, fmt.Errorf("%w: balance before: %v", ErrInvalidEntry, err)
	}
	if err := params.BalanceAfter.Validate(); err != nil {
		return nil, fmt.Errorf("%w: balance after: %v", ErrInvalidEntry, err)
	}
	if params.BalanceBefore.IsNegative() || params.BalanceAfter.IsNegative() {
		return nil, fmt.Errorf("%w: balances cannot be negative", ErrInvalidEntry)
	}
	if params.WalletVersion < 1 || params.CreatedAt.IsZero() {
		return nil, fmt.Errorf("%w: invalid version or timestamp", ErrInvalidEntry)
	}

	var expected money.Money
	var err error
	if params.Direction == DirectionCredit {
		expected, err = params.BalanceBefore.Add(params.Amount)
	} else {
		expected, err = params.BalanceBefore.Subtract(params.Amount)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBalanceEquation, err)
	}
	if !expected.Equal(params.BalanceAfter) {
		return nil, ErrBalanceEquation
	}

	return &Entry{
		id:            params.ID,
		walletID:      params.WalletID,
		transactionID: params.TransactionID,
		direction:     params.Direction,
		amount:        params.Amount,
		balanceBefore: params.BalanceBefore,
		balanceAfter:  params.BalanceAfter,
		walletVersion: params.WalletVersion,
		createdAt:     params.CreatedAt.UTC(),
	}, nil
}

func (e *Entry) ID() string                 { return e.id }
func (e *Entry) WalletID() string           { return e.walletID }
func (e *Entry) TransactionID() string      { return e.transactionID }
func (e *Entry) Direction() Direction       { return e.direction }
func (e *Entry) Amount() money.Money        { return e.amount }
func (e *Entry) BalanceBefore() money.Money { return e.balanceBefore }
func (e *Entry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e *Entry) WalletVersion() int64       { return e.walletVersion }
func (e *Entry) CreatedAt() time.Time       { return e.createdAt }

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
