package wallet

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
)

var (
	ErrInvalidWallet     = errors.New("invalid wallet")
	ErrNonPositiveAmount = errors.New("wallet movement amount must be positive")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrVersionOverflow   = errors.New("wallet version overflow")
)

type Wallet struct {
	id        string
	playerID  string
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

type Rehydration struct {
	ID        string
	PlayerID  string
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func New(id, playerID string, initialBalance money.Money, now time.Time) (*Wallet, error) {
	state := Rehydration{
		ID:        id,
		PlayerID:  playerID,
		Balance:   initialBalance,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}
	return build(state)
}

func Rehydrate(state Rehydration) (*Wallet, error) {
	return build(state)
}

func build(state Rehydration) (*Wallet, error) {
	if !validID(state.ID) || !validID(state.PlayerID) {
		return nil, fmt.Errorf("%w: id and player id are required", ErrInvalidWallet)
	}
	if err := state.Balance.Validate(); err != nil {
		return nil, fmt.Errorf("%w: balance: %v", ErrInvalidWallet, err)
	}
	if state.Balance.IsNegative() {
		return nil, fmt.Errorf("%w: negative balance", ErrInvalidWallet)
	}
	if state.Version < 1 {
		return nil, fmt.Errorf("%w: version must be at least 1", ErrInvalidWallet)
	}
	if state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() || state.UpdatedAt.Before(state.CreatedAt) {
		return nil, fmt.Errorf("%w: invalid timestamps", ErrInvalidWallet)
	}

	return &Wallet{
		id:        state.ID,
		playerID:  state.PlayerID,
		balance:   state.Balance,
		version:   state.Version,
		createdAt: state.CreatedAt.UTC(),
		updatedAt: state.UpdatedAt.UTC(),
	}, nil
}

func (w *Wallet) Credit(amount money.Money, now time.Time) (money.Money, money.Money, error) {
	if err := w.validateMovement(amount, now); err != nil {
		return money.Money{}, money.Money{}, err
	}
	result, err := w.balance.Add(amount)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}
	return w.apply(result, now)
}

func (w *Wallet) Debit(amount money.Money, now time.Time) (money.Money, money.Money, error) {
	if err := w.validateMovement(amount, now); err != nil {
		return money.Money{}, money.Money{}, err
	}
	comparison, err := w.balance.Compare(amount)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}
	if comparison < 0 {
		return money.Money{}, money.Money{}, ErrInsufficientFunds
	}
	result, err := w.balance.Subtract(amount)
	if err != nil {
		return money.Money{}, money.Money{}, err
	}
	return w.apply(result, now)
}

func (w *Wallet) validateMovement(amount money.Money, now time.Time) error {
	if w == nil {
		return ErrInvalidWallet
	}
	if !amount.IsPositive() {
		if err := amount.Validate(); err != nil {
			return err
		}
		return ErrNonPositiveAmount
	}
	if now.IsZero() || now.Before(w.updatedAt) {
		return fmt.Errorf("%w: movement time precedes wallet state", ErrInvalidWallet)
	}
	if w.version == math.MaxInt64 {
		return ErrVersionOverflow
	}
	_, err := w.balance.Compare(amount)
	return err
}

func (w *Wallet) apply(result money.Money, now time.Time) (money.Money, money.Money, error) {
	before := w.balance
	w.balance = result
	w.version++
	w.updatedAt = now.UTC()
	return before, result, nil
}

func (w *Wallet) ID() string               { return w.id }
func (w *Wallet) PlayerID() string         { return w.playerID }
func (w *Wallet) Balance() money.Money     { return w.balance }
func (w *Wallet) Version() int64           { return w.version }
func (w *Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time     { return w.updatedAt }
func (w *Wallet) Currency() money.Currency { return w.balance.Currency() }

func validID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}
