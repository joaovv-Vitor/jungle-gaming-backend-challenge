package wagering

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	domain "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
)

func canonicalPayload(input SubmitInput) ([]byte, domain.PayloadHash, error) {
	fields := []struct {
		name  string
		value string
	}{
		{"externalTransactionId", input.ExternalTransactionID},
		{"gameId", input.GameID},
		{"kind", string(input.Kind)},
	}
	var builder strings.Builder
	builder.WriteByte('{')
	for index, field := range fields {
		if index > 0 {
			builder.WriteByte(',')
		}
		if err := writeJSONField(&builder, field.name, field.value); err != nil {
			return nil, domain.PayloadHash{}, err
		}
	}
	builder.WriteString(`,"money":{"amount":`)
	amount, err := json.Marshal(input.Amount.String())
	if err != nil {
		return nil, domain.PayloadHash{}, err
	}
	builder.Write(amount)
	builder.WriteString(`,"currency":`)
	currency, err := json.Marshal(input.Amount.Currency().Code())
	if err != nil {
		return nil, domain.PayloadHash{}, err
	}
	builder.Write(currency)
	builder.WriteByte('}')
	for _, field := range []struct {
		name  string
		value string
	}{
		{"playerId", input.PlayerID},
		{"providerId", input.ProviderID},
	} {
		builder.WriteByte(',')
		if err := writeJSONField(&builder, field.name, field.value); err != nil {
			return nil, domain.PayloadHash{}, err
		}
	}
	if input.ReferenceExternalTransactionID != "" {
		builder.WriteByte(',')
		if err := writeJSONField(&builder, "referenceExternalTransactionId", input.ReferenceExternalTransactionID); err != nil {
			return nil, domain.PayloadHash{}, err
		}
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"roundId", input.RoundID},
		{"walletId", input.WalletID},
	} {
		builder.WriteByte(',')
		if err := writeJSONField(&builder, field.name, field.value); err != nil {
			return nil, domain.PayloadHash{}, err
		}
	}
	builder.WriteByte('}')
	payload := []byte(builder.String())
	return payload, domain.PayloadHash(sha256.Sum256(payload)), nil
}

func writeJSONField(builder *strings.Builder, name, value string) error {
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
