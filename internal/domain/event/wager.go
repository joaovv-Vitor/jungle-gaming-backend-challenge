package event

import (
	"fmt"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
)

type WagerTransactionProcessedData struct {
	TransactionID string          `json:"transactionId"`
	ProviderID    string          `json:"providerId,omitempty"`
	Kind          wagering.Kind   `json:"kind"`
	Status        wagering.Status `json:"status"`
	Money         money.Money     `json:"money"`
	Balance       money.Money     `json:"balance"`
}

type WagerTransactionRejectedData struct {
	TransactionID string               `json:"transactionId"`
	ProviderID    string               `json:"providerId"`
	Kind          wagering.Kind        `json:"kind"`
	Status        wagering.Status      `json:"status"`
	FailureCode   wagering.FailureCode `json:"failureCode"`
	Money         money.Money          `json:"money"`
	Balance       money.Money          `json:"balance"`
}

type WagerTransactionPendingReferenceData struct {
	TransactionID                  string          `json:"transactionId"`
	ProviderID                     string          `json:"providerId"`
	Kind                           wagering.Kind   `json:"kind"`
	Status                         wagering.Status `json:"status"`
	ReferenceExternalTransactionID string          `json:"referenceExternalTransactionId"`
}

type ProcessedInput struct {
	TransactionID string
	ProviderID    string
	Kind          wagering.Kind
	Money         money.Money
	Balance       money.Money
}

type RejectedInput struct {
	TransactionID string
	ProviderID    string
	Kind          wagering.Kind
	FailureCode   wagering.FailureCode
	Money         money.Money
	Balance       money.Money
}

type PendingReferenceInput struct {
	TransactionID                  string
	ProviderID                     string
	Kind                           wagering.Kind
	ReferenceExternalTransactionID string
}

func NewWagerTransactionProcessed(metadata Metadata, input ProcessedInput) (Envelope[WagerTransactionProcessedData], error) {
	if err := validateTransactionEvent(metadata, input.TransactionID, input.ProviderID, input.Kind, input.Money, input.Balance, true); err != nil {
		return Envelope[WagerTransactionProcessedData]{}, err
	}
	return newEnvelope(metadata, TypeWagerTransactionProcessed, WagerTransactionProcessedData{
		TransactionID: input.TransactionID,
		ProviderID:    input.ProviderID,
		Kind:          input.Kind,
		Status:        wagering.StatusProcessed,
		Money:         input.Money,
		Balance:       input.Balance,
	})
}

func NewWagerTransactionRejected(metadata Metadata, input RejectedInput) (Envelope[WagerTransactionRejectedData], error) {
	if input.Kind == wagering.KindOpening || !input.FailureCode.Valid() {
		return Envelope[WagerTransactionRejectedData]{}, fmt.Errorf("%w: rejection kind or failure code", ErrInvalidEvent)
	}
	if err := validateTransactionEvent(metadata, input.TransactionID, input.ProviderID, input.Kind, input.Money, input.Balance, false); err != nil {
		return Envelope[WagerTransactionRejectedData]{}, err
	}
	return newEnvelope(metadata, TypeWagerTransactionRejected, WagerTransactionRejectedData{
		TransactionID: input.TransactionID,
		ProviderID:    input.ProviderID,
		Kind:          input.Kind,
		Status:        wagering.StatusRejected,
		FailureCode:   input.FailureCode,
		Money:         input.Money,
		Balance:       input.Balance,
	})
}

func NewWagerTransactionPendingReference(metadata Metadata, input PendingReferenceInput) (Envelope[WagerTransactionPendingReferenceData], error) {
	if metadata.AggregateID != input.TransactionID || !validID(input.TransactionID) || !validID(input.ProviderID) ||
		!input.Kind.Valid() || input.Kind == wagering.KindOpening || !validID(input.ReferenceExternalTransactionID) {
		return Envelope[WagerTransactionPendingReferenceData]{}, fmt.Errorf("%w: pending reference data", ErrInvalidEvent)
	}
	if input.Kind != wagering.KindWin && input.Kind != wagering.KindRefund && input.Kind != wagering.KindRollback {
		return Envelope[WagerTransactionPendingReferenceData]{}, fmt.Errorf("%w: kind cannot wait for reference", ErrInvalidEvent)
	}
	return newEnvelope(metadata, TypeWagerTransactionPendingReference, WagerTransactionPendingReferenceData{
		TransactionID:                  input.TransactionID,
		ProviderID:                     input.ProviderID,
		Kind:                           input.Kind,
		Status:                         wagering.StatusPendingReference,
		ReferenceExternalTransactionID: input.ReferenceExternalTransactionID,
	})
}

func validateTransactionEvent(
	metadata Metadata,
	transactionID, providerID string,
	kind wagering.Kind,
	amount, balance money.Money,
	allowOpening bool,
) error {
	if metadata.AggregateID != transactionID || !validID(transactionID) || !kind.Valid() {
		return fmt.Errorf("%w: transaction identity or kind", ErrInvalidEvent)
	}
	if kind == wagering.KindOpening {
		if !allowOpening || providerID != "" || !amount.IsPositive() {
			return fmt.Errorf("%w: opening data", ErrInvalidEvent)
		}
	} else if !validID(providerID) {
		return fmt.Errorf("%w: provider id", ErrInvalidEvent)
	}
	if err := amount.Validate(); err != nil {
		return fmt.Errorf("%w: money", ErrInvalidEvent)
	}
	if err := balance.Validate(); err != nil || balance.IsNegative() {
		return fmt.Errorf("%w: balance", ErrInvalidEvent)
	}
	if amount.Currency() != balance.Currency() {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, money.ErrCurrencyMismatch)
	}
	if kind == wagering.KindLoss {
		if !amount.IsZero() {
			return fmt.Errorf("%w: LOSS amount", ErrInvalidEvent)
		}
	} else if !amount.IsPositive() {
		return fmt.Errorf("%w: operation amount", ErrInvalidEvent)
	}
	return nil
}
