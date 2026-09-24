package ingestion

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
)

const (
	MessageTypeWagerRequested = "WagerTransactionRequested"
	maxMessageBytes           = 256 * 1024
)

var ErrInvalidMessage = errors.New("invalid ingestion message")

type PayloadHash [sha256.Size]byte

type Message struct {
	ID         string
	Type       string
	OccurredAt time.Time
	Input      applicationwagering.SubmitInput
	hash       PayloadHash
}

func (m Message) Hash() PayloadHash { return m.hash }

type messageDTO struct {
	MessageID  string    `json:"messageId"`
	Type       string    `json:"type"`
	OccurredAt time.Time `json:"occurredAt"`
	Data       struct {
		ProviderID                     string      `json:"providerId"`
		ExternalTransactionID          string      `json:"externalTransactionId"`
		IdempotencyKey                 string      `json:"idempotencyKey"`
		PlayerID                       string      `json:"playerId"`
		WalletID                       string      `json:"walletId"`
		RoundID                        string      `json:"roundId"`
		GameID                         string      `json:"gameId"`
		Kind                           domain.Kind `json:"kind"`
		Money                          moneyDTO    `json:"money"`
		ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	} `json:"data"`
}

type moneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func DecodeMessage(body []byte) (Message, error) {
	if len(body) == 0 || len(body) > maxMessageBytes {
		return Message{}, ErrInvalidMessage
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var dto messageDTO
	if err := decoder.Decode(&dto); err != nil {
		return Message{}, fmt.Errorf("%w: decode: %v", ErrInvalidMessage, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Message{}, fmt.Errorf("%w: body must contain one JSON object", ErrInvalidMessage)
	}
	if strings.TrimSpace(dto.MessageID) != dto.MessageID || dto.MessageID == "" ||
		dto.Type != MessageTypeWagerRequested || dto.OccurredAt.IsZero() {
		return Message{}, ErrInvalidMessage
	}
	amount, err := money.ParseNonNegative(dto.Data.Money.Amount, dto.Data.Money.Currency)
	if err != nil {
		return Message{}, fmt.Errorf("%w: money: %v", ErrInvalidMessage, err)
	}
	input := applicationwagering.SubmitInput{
		ProviderID: dto.Data.ProviderID, ExternalTransactionID: dto.Data.ExternalTransactionID,
		IdempotencyKey: dto.Data.IdempotencyKey, WalletID: dto.Data.WalletID, PlayerID: dto.Data.PlayerID,
		RoundID: dto.Data.RoundID, GameID: dto.Data.GameID, Kind: dto.Data.Kind, Amount: amount,
		ReferenceExternalTransactionID: dto.Data.ReferenceExternalTransactionID,
		CorrelationID:                  dto.MessageID,
	}
	message := Message{ID: dto.MessageID, Type: dto.Type, OccurredAt: dto.OccurredAt.UTC(), Input: input}
	canonical, err := canonicalEnvelope(message)
	if err != nil {
		return Message{}, fmt.Errorf("%w: canonical envelope: %v", ErrInvalidMessage, err)
	}
	message.hash = PayloadHash(sha256.Sum256(canonical))
	return message, nil
}

func canonicalEnvelope(message Message) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString(`{"data":{`)
	fields := []struct{ name, value string }{
		{"externalTransactionId", message.Input.ExternalTransactionID},
		{"gameId", message.Input.GameID},
		{"idempotencyKey", message.Input.IdempotencyKey},
		{"kind", string(message.Input.Kind)},
	}
	for index, field := range fields {
		if index > 0 {
			builder.WriteByte(',')
		}
		if err := writeField(&builder, field.name, field.value); err != nil {
			return nil, err
		}
	}
	builder.WriteString(`,"money":{"amount":`)
	amount, err := json.Marshal(message.Input.Amount.String())
	if err != nil {
		return nil, err
	}
	builder.Write(amount)
	builder.WriteString(`,"currency":`)
	currency, err := json.Marshal(message.Input.Amount.Currency().Code())
	if err != nil {
		return nil, err
	}
	builder.Write(currency)
	builder.WriteByte('}')
	for _, field := range []struct{ name, value string }{
		{"playerId", message.Input.PlayerID}, {"providerId", message.Input.ProviderID},
	} {
		builder.WriteByte(',')
		if err := writeField(&builder, field.name, field.value); err != nil {
			return nil, err
		}
	}
	if message.Input.ReferenceExternalTransactionID != "" {
		builder.WriteByte(',')
		if err := writeField(&builder, "referenceExternalTransactionId", message.Input.ReferenceExternalTransactionID); err != nil {
			return nil, err
		}
	}
	for _, field := range []struct{ name, value string }{
		{"roundId", message.Input.RoundID}, {"walletId", message.Input.WalletID},
	} {
		builder.WriteByte(',')
		if err := writeField(&builder, field.name, field.value); err != nil {
			return nil, err
		}
	}
	builder.WriteByte('}')
	for _, field := range []struct{ name, value string }{
		{"messageId", message.ID},
		{"occurredAt", message.OccurredAt.UTC().Format(time.RFC3339Nano)},
		{"type", message.Type},
	} {
		builder.WriteByte(',')
		if err := writeField(&builder, field.name, field.value); err != nil {
			return nil, err
		}
	}
	builder.WriteByte('}')
	return []byte(builder.String()), nil
}

func writeField(builder *strings.Builder, name, value string) error {
	key, err := json.Marshal(name)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	builder.Write(key)
	builder.WriteByte(':')
	builder.Write(encoded)
	return nil
}
