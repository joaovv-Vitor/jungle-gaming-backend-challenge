package ingestion

import (
	"encoding/hex"
	"errors"
	"testing"
)

const validMessage = `{
  "messageId":"msg-123",
  "type":"WagerTransactionRequested",
  "occurredAt":"2026-09-08T12:00:00.000Z",
  "data":{
    "providerId":"provider-a",
    "externalTransactionId":"transaction-123",
    "idempotencyKey":"provider-a:transaction-123",
    "playerId":"10000000-0000-4000-8000-000000000001",
    "walletId":"20000000-0000-4000-8000-000000000001",
    "roundId":"round-987",
    "gameId":"fortune-chimp",
    "kind":"BET",
    "money":{"amount":"25.00","currency":"BRL"}
  }
}`

func TestDecodeMessageCanonicalHashVector(t *testing.T) {
	message, err := DecodeMessage([]byte(validMessage))
	if err != nil {
		t.Fatal(err)
	}
	if message.ID != "msg-123" || message.Input.CorrelationID != message.ID || message.Input.Amount.MinorUnits() != 2_500 {
		t.Fatalf("message = %+v", message)
	}
	hash := message.Hash()
	if got := hex.EncodeToString(hash[:]); got != "23208098d255f4bce1368e73b732ddfee441b7d901d8df13d0bc47227872d213" {
		t.Fatalf("hash = %s", got)
	}
}

func TestDecodeMessageNormalizesEquivalentTransportJSON(t *testing.T) {
	first, err := DecodeMessage([]byte(validMessage))
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeMessage([]byte(`{"type":"WagerTransactionRequested","data":{"money":{"currency":"BRL","amount":"025.00"},"kind":"BET","gameId":"fortune-chimp","roundId":"round-987","walletId":"20000000-0000-4000-8000-000000000001","playerId":"10000000-0000-4000-8000-000000000001","idempotencyKey":"provider-a:transaction-123","externalTransactionId":"transaction-123","providerId":"provider-a"},"occurredAt":"2026-09-08T09:00:00-03:00","messageId":"msg-123"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash() != second.Hash() {
		t.Fatal("equivalent envelopes produced different hashes")
	}
}

func TestDecodeMessageRejectsInvalidEnvelope(t *testing.T) {
	tests := []string{
		`{}`,
		validMessage + `{}`,
		`{"messageId":"msg-1","type":"Other","occurredAt":"2026-09-08T12:00:00Z","data":{}}`,
		`{"messageId":"msg-1","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","unknown":true,"data":{}}`,
	}
	for _, body := range tests {
		if _, err := DecodeMessage([]byte(body)); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("DecodeMessage(%s) error = %v", body, err)
		}
	}
}
