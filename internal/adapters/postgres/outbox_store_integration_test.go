//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	applicationoutbox "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
)

func TestOutboxPublishersClaimDistinctEventsAndRecoverAbandonedLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, _ := financialServices(t, ctx)
	defer pool.Close()
	secondPool, err := pgxpool.New(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	defer secondPool.Close()
	correlationID := "outbox-" + randomUUID(t)
	if _, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: correlationID,
	}); err != nil {
		t.Fatal(err)
	}
	forceOutboxDue(t, ctx, pool, correlationID)
	stores := []*OutboxStore{NewOutboxStore(NewUnitOfWork(pool)), NewOutboxStore(NewUnitOfWork(secondPool))}
	claims := make([]*applicationoutbox.Event, 2)
	errorsFound := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := range stores {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			claims[index], errorsFound[index] = stores[index].Claim(ctx, 30*time.Second)
		}(index)
	}
	close(start)
	group.Wait()
	for index := range claims {
		if errorsFound[index] != nil || claims[index] == nil {
			t.Fatalf("claim %d = %+v, error=%v", index, claims[index], errorsFound[index])
		}
	}
	if claims[0].ID == claims[1].ID || claims[0].GroupID != claims[1].GroupID || claims[0].Attempts != 1 || claims[1].Attempts != 1 {
		t.Fatalf("claims = %+v and %+v", claims[0], claims[1])
	}
	if err := stores[0].Confirm(ctx, *claims[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET locked_until=clock_timestamp()-interval '1 second' WHERE event_id=$1`, claims[1].ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := stores[0].Claim(ctx, 30*time.Second)
	if err != nil || reclaimed == nil || reclaimed.ID != claims[1].ID || reclaimed.Token == claims[1].Token || reclaimed.Attempts != 2 {
		t.Fatalf("reclaim = %+v, error=%v", reclaimed, err)
	}
	if err := stores[1].Confirm(ctx, *claims[1]); !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("stale confirmation = %v, want concurrent write", err)
	}
	if err := stores[0].Confirm(ctx, *reclaimed); err != nil {
		t.Fatal(err)
	}
	var published int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE correlation_id=$1 AND published_at IS NOT NULL`, correlationID).Scan(&published); err != nil || published != 2 {
		t.Fatalf("published = %d, error=%v, want 2", published, err)
	}
}

func TestOutboxFailedSendRetainsEventAndRetrySchedule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, walletService, _ := financialServices(t, ctx)
	defer pool.Close()
	correlationID := "outbox-retry-" + randomUUID(t)
	if _, err := walletService.Open(ctx, applicationwallet.OpenInput{
		PlayerID: randomUUID(t), Initial: mustMoney(t, 10_000, "BRL"), CorrelationID: correlationID,
	}); err != nil {
		t.Fatal(err)
	}
	forceOutboxDue(t, ctx, pool, correlationID)
	store := NewOutboxStore(NewUnitOfWork(pool))
	first, err := store.Claim(ctx, 30*time.Second)
	if err != nil || first == nil {
		t.Fatalf("claim = %+v, error=%v", first, err)
	}
	next := time.Now().Add(time.Minute)
	if err := store.Retry(ctx, *first, next, "destination unavailable"); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var lastError string
	var published bool
	var scheduled time.Time
	if err := pool.QueryRow(ctx, `SELECT attempts, last_error, next_attempt_at, published_at IS NOT NULL FROM outbox_events WHERE event_id=$1`, first.ID).Scan(&attempts, &lastError, &scheduled, &published); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastError != "destination unavailable" || published || scheduled.Before(next.Add(-time.Millisecond)) {
		t.Fatalf("retry state = attempts:%d error:%q next:%s published:%v", attempts, lastError, scheduled, published)
	}
	if err := store.Confirm(ctx, *first); !errors.Is(err, ErrConcurrentWrite) {
		t.Fatalf("old token confirmation = %v, want concurrent write", err)
	}
	forceOutboxDue(t, ctx, pool, correlationID)
	for range 2 {
		claimed, err := store.Claim(ctx, 30*time.Second)
		if err != nil || claimed == nil {
			t.Fatalf("retry claim = %+v, error=%v", claimed, err)
		}
		if err := store.Confirm(ctx, *claimed); err != nil {
			t.Fatal(err)
		}
	}
}

func forceOutboxDue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, correlationID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET next_attempt_at='2000-01-01' WHERE correlation_id=$1`, correlationID); err != nil {
		t.Fatal(err)
	}
}
