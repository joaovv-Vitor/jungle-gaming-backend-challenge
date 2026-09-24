package event

import (
	"fmt"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
)

type WalletBalanceChangedData struct {
	WalletID      string           `json:"walletId"`
	TransactionID string           `json:"transactionId"`
	Direction     ledger.Direction `json:"direction"`
	Money         money.Money      `json:"money"`
	BalanceBefore money.Money      `json:"balanceBefore"`
	BalanceAfter  money.Money      `json:"balanceAfter"`
	WalletVersion int64            `json:"walletVersion"`
}

func NewWalletBalanceChanged(metadata Metadata, data WalletBalanceChangedData) (Envelope[WalletBalanceChangedData], error) {
	if metadata.AggregateID != data.WalletID || !validID(data.WalletID) || !validID(data.TransactionID) ||
		!data.Direction.Valid() || !data.Money.IsPositive() || data.WalletVersion < 1 {
		return Envelope[WalletBalanceChangedData]{}, fmt.Errorf("%w: wallet balance data", ErrInvalidEvent)
	}
	if err := data.BalanceBefore.Validate(); err != nil || data.BalanceBefore.IsNegative() {
		return Envelope[WalletBalanceChangedData]{}, fmt.Errorf("%w: balance before", ErrInvalidEvent)
	}
	if err := data.BalanceAfter.Validate(); err != nil || data.BalanceAfter.IsNegative() {
		return Envelope[WalletBalanceChangedData]{}, fmt.Errorf("%w: balance after", ErrInvalidEvent)
	}

	var expected money.Money
	var err error
	if data.Direction == ledger.DirectionCredit {
		expected, err = data.BalanceBefore.Add(data.Money)
	} else {
		expected, err = data.BalanceBefore.Subtract(data.Money)
	}
	if err != nil || !expected.Equal(data.BalanceAfter) {
		return Envelope[WalletBalanceChangedData]{}, fmt.Errorf("%w: wallet balance equation", ErrInvalidEvent)
	}
	return newEnvelope(metadata, TypeWalletBalanceChanged, data)
}
