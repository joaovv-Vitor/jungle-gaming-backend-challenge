//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	applicationingestion "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/ingestion"
	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
)

func TestIngestionCommitsInboxAndFinancialEffectAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, wagerService := financialServices(t, ctx)
	defer pool.Close()
	ingestionService := applicationingestion.NewService(NewIngestionStore(
		NewUnitOfWork(pool), NewInboxRepository(), NewWalletRepository(), NewWagerRepository(), NewLedgerRepository(), NewOutboxRepository(),
	), wagerService)

	playerID := randomUUID(t)
	account, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	externalID := "sqs-bet-" + randomUUID(t)
	idempotencyKey := "sqs-key-" + randomUUID(t)
	message := decodeIngestionMessage(t, ingestionBody("message-1-"+randomUUID(t), account.ID(), playerID, externalID, idempotencyKey, "25.00"))

	first, err := ingestionService.Consume(ctx, "wager-transactions", message)
	if err != nil || first.Duplicate || first.WagerResult.Transaction.Status() != domain.StatusProcessed {
		t.Fatalf("first consume = %+v error=%v", first, err)
	}
	duplicate, err := ingestionService.Consume(ctx, "wager-transactions", message)
	if err != nil || !duplicate.Duplicate || duplicate.WagerResult.Transaction.ID() != first.WagerResult.Transaction.ID() {
		t.Fatalf("duplicate consume = %+v error=%v", duplicate, err)
	}
	assertInboxFinancialState(t, ctx, pool, walletService, account.ID(), message.ID, 7_500, 1, 2)

	conflicting := decodeIngestionMessage(t, ingestionBody(message.ID, account.ID(), playerID, externalID, idempotencyKey, "30.00"))
	if _, err := ingestionService.Consume(ctx, "wager-transactions", conflicting); !errors.Is(err, applicationingestion.ErrMessageConflict) {
		t.Fatalf("conflicting message error = %v", err)
	}
	assertInboxFinancialState(t, ctx, pool, walletService, account.ID(), message.ID, 7_500, 1, 2)
}

func TestIngestionSharesFinancialIdempotencyWithHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, wagerService := financialServices(t, ctx)
	defer pool.Close()
	ingestionService := applicationingestion.NewService(NewIngestionStore(
		NewUnitOfWork(pool), NewInboxRepository(), NewWalletRepository(), NewWagerRepository(), NewLedgerRepository(), NewOutboxRepository(),
	), wagerService)
	playerID := randomUUID(t)
	account, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	externalID := "cross-" + randomUUID(t)
	idempotencyKey := "cross-key-" + randomUUID(t)
	httpInput := applicationwagering.SubmitInput{
		ProviderID: "provider-a", ExternalTransactionID: externalID, IdempotencyKey: idempotencyKey,
		WalletID: account.ID(), PlayerID: playerID, RoundID: "round-1", GameID: "game-1",
		Kind: domain.KindBet, Amount: mustMoney(t, 1_000, "BRL"), CorrelationID: "http-first",
	}
	httpResult, err := wagerService.Submit(ctx, httpInput)
	if err != nil {
		t.Fatal(err)
	}
	message := decodeIngestionMessage(t, ingestionBody("message-cross-"+randomUUID(t), account.ID(), playerID, externalID, idempotencyKey, "10.00"))
	sqsResult, err := ingestionService.Consume(ctx, "wager-transactions", message)
	if err != nil || !sqsResult.WagerResult.Replay || sqsResult.WagerResult.Transaction.ID() != httpResult.Transaction.ID() {
		t.Fatalf("cross-transport result = %+v error=%v", sqsResult, err)
	}
	assertInboxFinancialState(t, ctx, pool, walletService, account.ID(), message.ID, 9_000, 1, 2)

	invalid := decodeIngestionMessage(t, ingestionBody("message-invalid-"+randomUUID(t), account.ID(), randomUUID(t), "invalid-"+randomUUID(t), "invalid-key-"+randomUUID(t), "5.00"))
	if _, err := ingestionService.Consume(ctx, "wager-transactions", invalid); !errors.Is(err, applicationwagering.ErrWalletMismatch) {
		t.Fatalf("invalid wallet identity error = %v", err)
	}
	var invalidInboxRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_inbox WHERE message_id=$1`, invalid.ID).Scan(&invalidInboxRows); err != nil {
		t.Fatal(err)
	}
	if invalidInboxRows != 0 {
		t.Fatalf("rolled-back inbox rows = %d, want 0", invalidInboxRows)
	}
}

func ingestionBody(messageID, walletID, playerID, externalID, idempotencyKey, amount string) string {
	return fmt.Sprintf(`{"messageId":%q,"type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":{"providerId":"provider-a","externalTransactionId":%q,"idempotencyKey":%q,"playerId":%q,"walletId":%q,"roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":%q,"currency":"BRL"}}}`,
		messageID, externalID, idempotencyKey, playerID, walletID, amount)
}

func decodeIngestionMessage(t *testing.T, body string) applicationingestion.Message {
	t.Helper()
	message, err := applicationingestion.DecodeMessage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func assertInboxFinancialState(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	walletService *applicationwallet.Service,
	walletID, messageID string,
	wantBalance int64,
	wantInbox, wantLedger int,
) {
	t.Helper()
	account, err := walletService.FindByID(ctx, walletID)
	if err != nil {
		t.Fatal(err)
	}
	if account.Balance().MinorUnits() != wantBalance {
		t.Fatalf("balance = %d, want %d", account.Balance().MinorUnits(), wantBalance)
	}
	var inboxRows, ledgerRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM consumer_inbox WHERE message_id=$1 AND completed_at IS NOT NULL`, messageID).Scan(&inboxRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, walletID).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if inboxRows != wantInbox || ledgerRows != wantLedger {
		t.Fatalf("inbox/ledger rows = %d/%d, want %d/%d", inboxRows, ledgerRows, wantInbox, wantLedger)
	}
}
