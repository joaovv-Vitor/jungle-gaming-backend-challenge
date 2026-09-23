package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/ledger"
	walletdomain "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/domain/wallet"
)

type WalletStore struct {
	unit    *UnitOfWork
	wallets *WalletRepository
	wagers  *WagerRepository
	ledger  *LedgerRepository
	outbox  *OutboxRepository
}

func NewWalletStore(
	unit *UnitOfWork,
	wallets *WalletRepository,
	wagers *WagerRepository,
	ledger *LedgerRepository,
	outbox *OutboxRepository,
) *WalletStore {
	return &WalletStore{unit: unit, wallets: wallets, wagers: wagers, ledger: ledger, outbox: outbox}
}

func (s *WalletStore) Create(ctx context.Context, creation applicationwallet.Creation) error {
	err := s.unit.ReadCommittedWithRetry(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.wallets.Insert(ctx, tx, creation.Wallet); err != nil {
			return err
		}
		if creation.Opening == nil {
			if creation.Ledger != nil || len(creation.Events) != 0 {
				return errors.New("zero-balance wallet cannot contain financial records")
			}
			return nil
		}
		if creation.Ledger == nil {
			return errors.New("positive opening requires ledger entry")
		}
		if err := s.wagers.Insert(ctx, tx, creation.Opening, nil); err != nil {
			return err
		}
		if err := s.ledger.Insert(ctx, tx, creation.Ledger); err != nil {
			return err
		}
		for _, domainEvent := range creation.Events {
			if err := s.outbox.Insert(ctx, tx, domainEvent); err != nil {
				return err
			}
		}
		return nil
	})
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" && postgresError.ConstraintName == "wallets_player_currency_key" {
		return applicationwallet.ErrWalletAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("create wallet transaction: %w", err)
	}
	return nil
}

func (s *WalletStore) FindByID(ctx context.Context, walletID string) (account *walletdomain.Wallet, err error) {
	err = s.unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		account, err = s.wallets.FindByID(ctx, tx, walletID)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return nil, applicationwallet.ErrWalletNotFound
	}
	return account, err
}

func (s *WalletStore) ListLedger(
	ctx context.Context,
	walletID string,
	after *applicationwallet.LedgerCursor,
	limit int,
) (entries []*ledger.Entry, err error) {
	err = s.unit.ReadCommitted(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.wallets.FindByID(ctx, tx, walletID); err != nil {
			return err
		}
		var cursor *LedgerCursor
		if after != nil {
			cursor = &LedgerCursor{WalletVersion: after.WalletVersion, EntryID: after.EntryID}
		}
		entries, err = s.ledger.ListByWallet(ctx, tx, walletID, cursor, limit)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return nil, applicationwallet.ErrWalletNotFound
	}
	return entries, err
}

var _ applicationwallet.Store = (*WalletStore)(nil)
