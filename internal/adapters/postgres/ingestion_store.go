package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/ingestion"
	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
)

type IngestionStore struct {
	unit   *UnitOfWork
	inbox  *InboxRepository
	wagers *WagerStore
}

func NewIngestionStore(
	unit *UnitOfWork,
	inbox *InboxRepository,
	wallets *WalletRepository,
	wagers *WagerRepository,
	ledger *LedgerRepository,
	outbox *OutboxRepository,
) *IngestionStore {
	return &IngestionStore{
		unit: unit, inbox: inbox,
		wagers: NewWagerStore(unit, wallets, wagers, ledger, outbox),
	}
}

func (s *IngestionStore) WithinTransaction(ctx context.Context, work func(application.Session) error) error {
	err := s.unit.ReadCommittedWithRetry(ctx, func(ctx context.Context, tx pgx.Tx) error {
		wagering := wagerSession{tx: tx, store: s.wagers}
		return work(ingestionSession{wagerSession: wagering, inbox: s.inbox})
	})
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.ConstraintName {
		case "wager_provider_idempotency_key", "wager_provider_external_transaction_key":
			return applicationwagering.ErrIdentityRace
		}
	}
	return err
}

type ingestionSession struct {
	wagerSession
	inbox *InboxRepository
}

func (s ingestionSession) ClaimMessage(
	ctx context.Context,
	consumer, messageID string,
	hash application.PayloadHash,
	receivedAt time.Time,
) (application.Claim, error) {
	return s.inbox.Claim(ctx, s.tx, consumer, messageID, hash, receivedAt)
}

func (s ingestionSession) CompleteMessage(
	ctx context.Context,
	consumer, messageID, transactionID string,
	completedAt time.Time,
) error {
	return s.inbox.Complete(ctx, s.tx, consumer, messageID, transactionID, completedAt)
}

var _ application.Store = (*IngestionStore)(nil)
var _ application.Session = ingestionSession{}
