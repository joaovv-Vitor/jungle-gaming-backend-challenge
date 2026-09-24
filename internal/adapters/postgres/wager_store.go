package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/event"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	domain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wallet"
)

type WagerStore struct {
	unit    *UnitOfWork
	wallets *WalletRepository
	wagers  *WagerRepository
	ledger  *LedgerRepository
	outbox  *OutboxRepository
}

func NewWagerStore(
	unit *UnitOfWork,
	wallets *WalletRepository,
	wagers *WagerRepository,
	ledger *LedgerRepository,
	outbox *OutboxRepository,
) *WagerStore {
	return &WagerStore{unit: unit, wallets: wallets, wagers: wagers, ledger: ledger, outbox: outbox}
}

func (s *WagerStore) WithinTransaction(ctx context.Context, work func(application.Session) error) error {
	err := s.unit.ReadCommittedWithRetry(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return work(wagerSession{tx: tx, store: s})
	})
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.ConstraintName {
		case "wager_provider_idempotency_key", "wager_provider_external_transaction_key":
			return application.ErrIdentityRace
		}
	}
	return err
}

type wagerSession struct {
	tx           pgx.Tx
	store        *WagerStore
	pendingToken string
}

func (s wagerSession) FindByID(ctx context.Context, id string) (*domain.Transaction, error) {
	transaction, err := s.store.wagers.FindByID(ctx, s.tx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return transaction, err
}

func (s wagerSession) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*domain.Transaction, error) {
	transaction, err := s.store.wagers.FindByProviderIdempotencyKey(ctx, s.tx, providerID, key)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return transaction, err
}

func (s wagerSession) FindByExternalID(ctx context.Context, providerID, externalID string) (*domain.Transaction, error) {
	transaction, err := s.store.wagers.FindByProviderExternalID(ctx, s.tx, providerID, externalID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return transaction, err
}

func (s wagerSession) LockWallet(ctx context.Context, walletID string) (*wallet.Wallet, error) {
	account, err := s.store.wallets.FindByIDForUpdate(ctx, s.tx, walletID)
	if errors.Is(err, ErrNotFound) {
		return nil, application.ErrWalletNotFound
	}
	return account, err
}

func (s wagerSession) HasProcessedReversal(ctx context.Context, referenceTransactionID string) (bool, error) {
	return s.store.wagers.HasProcessedReversal(ctx, s.tx, referenceTransactionID)
}

func (s wagerSession) InsertTransaction(ctx context.Context, transaction *domain.Transaction, schedule *application.ReferenceSchedule) error {
	var persistenceSchedule *ReferenceSchedule
	if schedule != nil {
		persistenceSchedule = &ReferenceSchedule{NextAttemptAt: schedule.NextAttemptAt, ExpiresAt: schedule.ExpiresAt}
	}
	return s.store.wagers.Insert(ctx, s.tx, transaction, persistenceSchedule)
}

func (s wagerSession) UpdateTransaction(ctx context.Context, transaction *domain.Transaction) error {
	return s.store.wagers.CompletePending(ctx, s.tx, transaction, s.pendingToken)
}

func (s wagerSession) UpdateWallet(ctx context.Context, account *wallet.Wallet) error {
	err := s.store.wallets.Update(ctx, s.tx, account)
	if errors.Is(err, ErrConcurrentWrite) {
		return application.ErrConcurrentWrite
	}
	return err
}

func (s wagerSession) InsertLedger(ctx context.Context, entry *ledger.Entry) error {
	return s.store.ledger.Insert(ctx, s.tx, entry)
}

func (s wagerSession) InsertEvent(ctx context.Context, domainEvent event.IntegrationEvent) error {
	return s.store.outbox.Insert(ctx, s.tx, domainEvent)
}

var _ application.Store = (*WagerStore)(nil)
var _ application.Session = wagerSession{}
