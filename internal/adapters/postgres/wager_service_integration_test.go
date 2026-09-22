//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/money"
	domain "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wagering"
)

func TestWagerServiceSerializesCompetingBets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, wagerService := financialServices(t, ctx)
	defer pool.Close()
	secondPool, secondWagerService := wagerServiceWithIndependentPool(t, ctx)
	defer secondPool.Close()
	wagerServices := []*applicationwagering.Service{wagerService, secondWagerService}
	playerID := randomUUID(t)
	account, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	inputs := []applicationwagering.SubmitInput{
		wagerInput(t, account.ID(), playerID, "bet-a-"+randomUUID(t), domain.KindBet, mustMoney(t, 8_000, "BRL"), ""),
		wagerInput(t, account.ID(), playerID, "bet-b-"+randomUUID(t), domain.KindBet, mustMoney(t, 8_000, "BRL"), ""),
	}
	start := make(chan struct{})
	results := make([]applicationwagering.Result, len(inputs))
	errorsFound := make([]error, len(inputs))
	var group sync.WaitGroup
	for index := range inputs {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], errorsFound[index] = wagerServices[index].Submit(ctx, inputs[index])
		}(index)
	}
	close(start)
	group.Wait()
	processed, rejected := 0, 0
	processedIndex := 0
	for index, result := range results {
		if errorsFound[index] != nil {
			t.Fatalf("Submit(%d) error = %v", index, errorsFound[index])
		}
		switch result.Transaction.Status() {
		case domain.StatusProcessed:
			processed++
			processedIndex = index
		case domain.StatusRejected:
			rejected++
			if result.Transaction.FailureCode() != domain.FailureBetInsufficientFunds {
				t.Fatalf("rejection code = %s", result.Transaction.FailureCode())
			}
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed/rejected = %d/%d, want 1/1", processed, rejected)
	}
	stored, err := walletService.FindByID(ctx, account.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Balance().MinorUnits() != 2_000 || stored.Version() != 2 {
		t.Fatalf("wallet = %d/v%d, want 2000/v2", stored.Balance().MinorUnits(), stored.Version())
	}
	page, err := walletService.ListLedger(ctx, account.ID(), "", 50)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("ledger entries = %d error=%v, want 2", len(page.Entries), err)
	}

	replay, err := wagerService.Submit(ctx, inputs[processedIndex])
	if err != nil || !replay.Replay || replay.Transaction.ID() != results[processedIndex].Transaction.ID() {
		t.Fatalf("replay = %+v error=%v", replay, err)
	}
	conflicting := inputs[processedIndex]
	conflicting.Amount = mustMoney(t, 7_000, "BRL")
	if _, err := wagerService.Submit(ctx, conflicting); !errors.Is(err, applicationwagering.ErrIdempotencyConflict) {
		t.Fatalf("payload conflict error = %v", err)
	}
	conflicting = inputs[processedIndex]
	conflicting.IdempotencyKey = "another-key-" + randomUUID(t)
	if _, err := wagerService.Submit(ctx, conflicting); !errors.Is(err, applicationwagering.ErrExternalIDConflict) {
		t.Fatalf("external id conflict error = %v", err)
	}

	storedTransaction, err := wagerService.FindByID(ctx, "provider-a", results[processedIndex].Transaction.ID())
	if err != nil || storedTransaction.ID() != results[processedIndex].Transaction.ID() {
		t.Fatalf("FindByID() transaction = %+v error=%v", storedTransaction, err)
	}
	if _, err := wagerService.FindByID(ctx, "provider-b", results[processedIndex].Transaction.ID()); !errors.Is(err, applicationwagering.ErrTransactionNotFound) {
		t.Fatalf("FindByID() cross-provider error = %v", err)
	}
	storedTransaction, err = wagerService.FindByExternalID(ctx, "provider-a", inputs[processedIndex].ExternalTransactionID)
	if err != nil || storedTransaction.ID() != results[processedIndex].Transaction.ID() {
		t.Fatalf("FindByExternalID() transaction = %+v error=%v", storedTransaction, err)
	}
	if _, err := wagerService.FindByExternalID(ctx, "provider-b", inputs[processedIndex].ExternalTransactionID); !errors.Is(err, applicationwagering.ErrTransactionNotFound) {
		t.Fatalf("FindByExternalID() cross-provider error = %v", err)
	}
}

func TestWagerServiceDeduplicatesFiftyConcurrentDeliveries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, walletService, wagerService := financialServices(t, ctx)
	defer pool.Close()
	secondPool, secondWagerService := wagerServiceWithIndependentPool(t, ctx)
	defer secondPool.Close()
	thirdPool, thirdWagerService := wagerServiceWithIndependentPool(t, ctx)
	defer thirdPool.Close()
	wagerServices := []*applicationwagering.Service{wagerService, secondWagerService, thirdWagerService}
	playerID := randomUUID(t)
	account, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	input := wagerInput(t, account.ID(), playerID, "duplicate-"+randomUUID(t), domain.KindBet, mustMoney(t, 2_500, "BRL"), "")

	const deliveries = 50
	start := make(chan struct{})
	results := make([]applicationwagering.Result, deliveries)
	errorsFound := make([]error, deliveries)
	var group sync.WaitGroup
	for index := range deliveries {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results[index], errorsFound[index] = wagerServices[index%len(wagerServices)].Submit(ctx, input)
		}()
	}
	close(start)
	group.Wait()

	transactionID := ""
	firstDeliveries := 0
	for index, result := range results {
		if errorsFound[index] != nil {
			t.Fatalf("Submit(%d) error = %v", index, errorsFound[index])
		}
		if result.Transaction.Status() != domain.StatusProcessed {
			t.Fatalf("Submit(%d) status = %s", index, result.Transaction.Status())
		}
		if transactionID == "" {
			transactionID = result.Transaction.ID()
		}
		if result.Transaction.ID() != transactionID {
			t.Fatalf("Submit(%d) transaction ID = %s, want %s", index, result.Transaction.ID(), transactionID)
		}
		if !result.Replay {
			firstDeliveries++
		}
	}
	if firstDeliveries != 1 {
		t.Fatalf("non-replay deliveries = %d, want 1", firstDeliveries)
	}

	stored, err := walletService.FindByID(ctx, account.ID())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Balance().MinorUnits() != 7_500 || stored.Version() != 2 {
		t.Fatalf("wallet = %d/v%d, want 7500/v2", stored.Balance().MinorUnits(), stored.Version())
	}
	page, err := walletService.ListLedger(ctx, account.ID(), "", 50)
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("ledger entries = %d error=%v, want 2", len(page.Entries), err)
	}
	var outboxEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE correlation_id = $1`, input.CorrelationID).Scan(&outboxEvents); err != nil {
		t.Fatal(err)
	}
	if outboxEvents != 2 {
		t.Fatalf("outbox events = %d, want 2", outboxEvents)
	}
}

func TestWagerServiceProcessesAllKindsAndPendingReference(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, wagerService := financialServices(t, ctx)
	defer pool.Close()
	playerID := randomUUID(t)
	account, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}

	betExternalID := "bet-" + randomUUID(t)
	bet := wagerInput(t, account.ID(), playerID, betExternalID, domain.KindBet, mustMoney(t, 2_000, "BRL"), "")
	assertWagerStatus(t, ctx, wagerService, bet, domain.StatusProcessed, 8_000)

	winExternalID := "win-" + randomUUID(t)
	win := wagerInput(t, account.ID(), playerID, winExternalID, domain.KindWin, mustMoney(t, 1_000, "BRL"), betExternalID)
	assertWagerStatus(t, ctx, wagerService, win, domain.StatusProcessed, 9_000)

	loss := wagerInput(t, account.ID(), playerID, "loss-"+randomUUID(t), domain.KindLoss, mustMoney(t, 0, "BRL"), "")
	assertWagerStatus(t, ctx, wagerService, loss, domain.StatusProcessed, 9_000)

	refund := wagerInput(t, account.ID(), playerID, "refund-"+randomUUID(t), domain.KindRefund, mustMoney(t, 2_000, "BRL"), betExternalID)
	assertWagerStatus(t, ctx, wagerService, refund, domain.StatusProcessed, 11_000)

	rollback := wagerInput(t, account.ID(), playerID, "rollback-"+randomUUID(t), domain.KindRollback, mustMoney(t, 1_000, "BRL"), winExternalID)
	assertWagerStatus(t, ctx, wagerService, rollback, domain.StatusProcessed, 10_000)

	secondReversal := wagerInput(t, account.ID(), playerID, "refund-2-"+randomUUID(t), domain.KindRefund, mustMoney(t, 2_000, "BRL"), betExternalID)
	result, err := wagerService.Submit(ctx, secondReversal)
	if err != nil || result.Transaction.Status() != domain.StatusRejected || result.Transaction.FailureCode() != domain.FailureAlreadyReversed {
		t.Fatalf("second reversal = %+v error=%v", result, err)
	}

	pending := wagerInput(t, account.ID(), playerID, "pending-"+randomUUID(t), domain.KindRefund, mustMoney(t, 500, "BRL"), "missing-reference")
	result, err = wagerService.Submit(ctx, pending)
	if err != nil || result.Transaction.Status() != domain.StatusPendingReference {
		t.Fatalf("pending reference = %+v error=%v", result, err)
	}

	replay, err := wagerService.Submit(ctx, bet)
	if err != nil || !replay.Replay {
		t.Fatalf("historical replay = %+v error=%v", replay, err)
	}
	historicalBalance, present := replay.Transaction.ResultBalance()
	if !present || historicalBalance.MinorUnits() != 8_000 {
		t.Fatalf("historical replay balance = %d/%v, want 8000/true", historicalBalance.MinorUnits(), present)
	}
}

func financialServices(
	t *testing.T,
	ctx context.Context,
) (*pgxpool.Pool, *applicationwallet.Service, *applicationwagering.Service) {
	t.Helper()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	unit := NewUnitOfWork(pool)
	wallets := NewWalletRepository()
	wagers := NewWagerRepository()
	entries := NewLedgerRepository()
	outbox := NewOutboxRepository()
	return pool,
		applicationwallet.NewService(NewWalletStore(unit, wallets, wagers, entries, outbox)),
		applicationwagering.NewService(NewWagerStore(unit, wallets, wagers, entries, outbox))
}

func wagerServiceWithIndependentPool(
	t *testing.T,
	ctx context.Context,
) (*pgxpool.Pool, *applicationwagering.Service) {
	t.Helper()
	pool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	return pool, applicationwagering.NewService(NewWagerStore(
		NewUnitOfWork(pool),
		NewWalletRepository(),
		NewWagerRepository(),
		NewLedgerRepository(),
		NewOutboxRepository(),
	))
}

func wagerInput(
	t *testing.T,
	walletID, playerID, externalID string,
	kind domain.Kind,
	amount money.Money,
	reference string,
) applicationwagering.SubmitInput {
	t.Helper()
	return applicationwagering.SubmitInput{
		ProviderID: "provider-a", ExternalTransactionID: externalID,
		IdempotencyKey: "key-" + externalID, WalletID: walletID, PlayerID: playerID,
		RoundID: "round-1", GameID: "game-1", Kind: kind, Amount: amount,
		ReferenceExternalTransactionID: reference, CorrelationID: "correlation-" + externalID,
	}
}

func assertWagerStatus(
	t *testing.T,
	ctx context.Context,
	service *applicationwagering.Service,
	input applicationwagering.SubmitInput,
	wantStatus domain.Status,
	wantBalance int64,
) {
	t.Helper()
	result, err := service.Submit(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	balance, present := result.Transaction.ResultBalance()
	if result.Transaction.Status() != wantStatus || !present || balance.MinorUnits() != wantBalance {
		t.Fatalf("%s result = status:%s balance:%d/%v, want %s/%d", input.Kind, result.Transaction.Status(), balance.MinorUnits(), present, wantStatus, wantBalance)
	}
}
