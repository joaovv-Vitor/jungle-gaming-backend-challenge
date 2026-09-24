package wagering

import (
	"encoding/hex"
	"testing"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
)

func TestCanonicalPayloadVector(t *testing.T) {
	amount, _ := money.Parse("25.00", "BRL")
	input := SubmitInput{
		ProviderID: "provider-a", ExternalTransactionID: "tx-1",
		WalletID: "20000000-0000-4000-8000-000000000001",
		PlayerID: "10000000-0000-4000-8000-000000000001",
		RoundID:  "round-1", GameID: "game-1", Kind: domain.KindBet, Amount: amount,
	}
	payload, hash, err := canonicalPayload(input)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload := `{"externalTransactionId":"tx-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"10000000-0000-4000-8000-000000000001","providerId":"provider-a","roundId":"round-1","walletId":"20000000-0000-4000-8000-000000000001"}`
	if string(payload) != wantPayload {
		t.Fatalf("canonical payload = %s\nwant = %s", payload, wantPayload)
	}
	if got := hex.EncodeToString(hash[:]); got != "2cc3fe98416f70599659083da74c0a61cea258eda709e0804463c6b74c8367ae" {
		t.Fatalf("hash = %s", got)
	}
}

func TestCanonicalPayloadExcludesIdempotencyAndCorrelation(t *testing.T) {
	amount, _ := money.Parse("1.00", "BRL")
	base := SubmitInput{
		ProviderID: "provider-a", ExternalTransactionID: "tx-1", IdempotencyKey: "key-1",
		WalletID: "20000000-0000-4000-8000-000000000001",
		PlayerID: "10000000-0000-4000-8000-000000000001",
		RoundID:  "round-1", GameID: "game-1", Kind: domain.KindBet, Amount: amount,
		CorrelationID: "correlation-1",
	}
	_, first, _ := canonicalPayload(base)
	base.IdempotencyKey = "key-2"
	base.CorrelationID = "correlation-2"
	_, second, _ := canonicalPayload(base)
	if first != second {
		t.Fatal("transport metadata changed the business payload hash")
	}
}
