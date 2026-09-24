//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
)

func TestRefundAndRollbackRaceForSameBet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, firstService := financialServices(t, ctx)
	defer pool.Close()
	secondPool, secondService := wagerServiceWithIndependentPool(t, ctx)
	defer secondPool.Close()
	playerID := randomUUID(t)
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	bet := wagerInput(t, account.ID(), playerID, "bet-"+randomUUID(t), domain.KindBet, mustMoney(t, 2_000, "BRL"), "")
	assertWagerStatus(t, ctx, firstService, bet, domain.StatusProcessed, 8_000)
	inputs := []applicationwagering.SubmitInput{
		wagerInput(t, account.ID(), playerID, "refund-"+randomUUID(t), domain.KindRefund, mustMoney(t, 2_000, "BRL"), bet.ExternalTransactionID),
		wagerInput(t, account.ID(), playerID, "rollback-"+randomUUID(t), domain.KindRollback, mustMoney(t, 2_000, "BRL"), bet.ExternalTransactionID),
	}
	services := []*applicationwagering.Service{firstService, secondService}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err := lock.Exec(ctx, `SELECT id FROM wallets WHERE id=$1 FOR NO KEY UPDATE`, account.ID()); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make([]applicationwagering.Result, 2)
	errorsFound := make([]error, 2)
	var group sync.WaitGroup
	for index := range inputs {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			results[index], errorsFound[index] = services[index].Submit(ctx, inputs[index])
		}(index)
	}
	close(start)
	awaitTwoWalletLockWaiters(t, ctx, pool)
	if err := lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	group.Wait()
	processed, rejected := 0, 0
	var loser applicationwagering.SubmitInput
	var loserBalance int64
	for index, result := range results {
		if errorsFound[index] != nil {
			t.Fatalf("reversal %d error = %v", index, errorsFound[index])
		}
		balance, present := result.Transaction.ResultBalance()
		if !present || balance.MinorUnits() != 10_000 {
			t.Fatalf("reversal %d balance = %d/%v, want 10000/true", index, balance.MinorUnits(), present)
		}
		switch result.Transaction.Status() {
		case domain.StatusProcessed:
			processed++
		case domain.StatusRejected:
			rejected++
			loser = inputs[index]
			loserBalance = balance.MinorUnits()
			if result.Transaction.FailureCode() != domain.FailureAlreadyReversed {
				t.Fatalf("losing reversal failure = %s", result.Transaction.FailureCode())
			}
		default:
			t.Fatalf("reversal %d status = %s", index, result.Transaction.Status())
		}
	}
	if processed != 1 || rejected != 1 {
		t.Fatalf("processed/rejected = %d/%d, want 1/1", processed, rejected)
	}
	var successfulReversals, ledgerRows, eventRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE reference_transaction_id=(SELECT id FROM wager_transactions WHERE provider_id=$1 AND external_transaction_id=$2) AND status='PROCESSED' AND kind IN ('REFUND','ROLLBACK')`, bet.ProviderID, bet.ExternalTransactionID).Scan(&successfulReversals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, account.ID()).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 OR aggregate_id IN (SELECT id FROM wager_transactions WHERE wallet_id=$1)`, account.ID()).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	stored, err := wallets.FindByID(ctx, account.ID())
	if err != nil {
		t.Fatal(err)
	}
	if successfulReversals != 1 || ledgerRows != 3 || eventRows != 7 || stored.Balance().MinorUnits() != 10_000 || stored.Version() != 3 {
		t.Fatalf("state: reversals=%d ledger=%d events=%d balance=%d version=%d", successfulReversals, ledgerRows, eventRows, stored.Balance().MinorUnits(), stored.Version())
	}
	win := wagerInput(t, account.ID(), playerID, "win-"+randomUUID(t), domain.KindWin, mustMoney(t, 500, "BRL"), "")
	assertWagerStatus(t, ctx, firstService, win, domain.StatusProcessed, 10_500)
	replay, err := secondService.Submit(ctx, loser)
	if err != nil || !replay.Replay || replay.Transaction.Status() != domain.StatusRejected || replay.Transaction.FailureCode() != domain.FailureAlreadyReversed {
		t.Fatalf("rejected replay = %+v, error=%v", replay, err)
	}
	replayedBalance, present := replay.Transaction.ResultBalance()
	if !present || replayedBalance.MinorUnits() != loserBalance {
		t.Fatalf("rejected historical balance = %d/%v, want %d/true", replayedBalance.MinorUnits(), present, loserBalance)
	}
}

func TestRollbackOfWinRejectsWhenBalanceIsInsufficient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, wallets, wagers := financialServices(t, ctx)
	defer pool.Close()
	playerID := randomUUID(t)
	account, err := wallets.Open(ctx, applicationwallet.OpenInput{
		PlayerID: playerID, Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: "open-" + randomUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	win := wagerInput(t, account.ID(), playerID, "win-"+randomUUID(t), domain.KindWin, mustMoney(t, 2_000, "BRL"), "")
	assertWagerStatus(t, ctx, wagers, win, domain.StatusProcessed, 12_000)
	bet := wagerInput(t, account.ID(), playerID, "bet-"+randomUUID(t), domain.KindBet, mustMoney(t, 11_000, "BRL"), "")
	assertWagerStatus(t, ctx, wagers, bet, domain.StatusProcessed, 1_000)
	rollback := wagerInput(t, account.ID(), playerID, "rollback-"+randomUUID(t), domain.KindRollback, mustMoney(t, 2_000, "BRL"), win.ExternalTransactionID)
	result, err := wagers.Submit(ctx, rollback)
	if err != nil || result.Transaction.Status() != domain.StatusRejected || result.Transaction.FailureCode() != domain.FailureReversalInsufficientFunds {
		t.Fatalf("rollback = %+v, error=%v", result, err)
	}
	stored, err := wallets.FindByID(ctx, account.ID())
	if err != nil {
		t.Fatal(err)
	}
	var ledgerRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id=$1`, account.ID()).Scan(&ledgerRows); err != nil {
		t.Fatal(err)
	}
	if stored.Balance().MinorUnits() != 1_000 || stored.Version() != 3 || ledgerRows != 3 {
		t.Fatalf("state: balance=%d version=%d ledger=%d", stored.Balance().MinorUnits(), stored.Version(), ledgerRows)
	}
}

func awaitTwoWalletLockWaiters(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FOR NO KEY UPDATE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("observed %d wallet lock waiters, want two", waiting)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
