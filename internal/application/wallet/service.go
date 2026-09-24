package wallet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/event"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/ledger"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wagering"
	walletdomain "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/wallet"
	platformid "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/id"
)

var (
	ErrWalletAlreadyExists = errors.New("wallet already exists for player and currency")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrInvalidInput        = errors.New("invalid wallet input")
)

type Creation struct {
	Wallet  *walletdomain.Wallet
	Opening *wagering.Transaction
	Ledger  *ledger.Entry
	Events  []event.IntegrationEvent
}

type Store interface {
	Create(context.Context, Creation) error
	FindByID(context.Context, string) (*walletdomain.Wallet, error)
	ListLedger(context.Context, string, *LedgerCursor, int) ([]*ledger.Entry, error)
}

type LedgerCursor struct {
	FormatVersion int    `json:"version"`
	WalletID      string `json:"walletId"`
	WalletVersion int64  `json:"walletVersion"`
	EntryID       string `json:"entryId"`
}

const ledgerCursorFormatVersion = 1

type LedgerPage struct {
	Entries    []*ledger.Entry
	NextCursor string
}

type OpenInput struct {
	PlayerID      string
	Initial       money.Money
	CorrelationID string
}

type Service struct {
	store Store
	now   func() time.Time
	newID func() (string, error)
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: platformid.New}
}

func (s *Service) Open(ctx context.Context, input OpenInput) (*walletdomain.Wallet, error) {
	if err := platformid.Validate(input.PlayerID); err != nil {
		return nil, fmt.Errorf("%w: player id: %v", ErrInvalidInput, err)
	}
	if input.CorrelationID == "" {
		return nil, fmt.Errorf("%w: correlation id is required", ErrInvalidInput)
	}
	if err := input.Initial.Validate(); err != nil || input.Initial.IsNegative() {
		return nil, fmt.Errorf("%w: initial balance", ErrInvalidInput)
	}
	walletID, err := s.newID()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	account, err := walletdomain.New(walletID, input.PlayerID, input.Initial, now)
	if err != nil {
		return nil, err
	}
	creation := Creation{Wallet: account}
	if input.Initial.IsPositive() {
		if err := s.addOpening(&creation, input, now); err != nil {
			return nil, err
		}
	}
	if err := s.store.Create(ctx, creation); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *Service) FindByID(ctx context.Context, walletID string) (*walletdomain.Wallet, error) {
	if err := platformid.Validate(walletID); err != nil {
		return nil, fmt.Errorf("%w: wallet id", ErrInvalidInput)
	}
	return s.store.FindByID(ctx, walletID)
}

func (s *Service) ListLedger(ctx context.Context, walletID, opaqueCursor string, limit int) (LedgerPage, error) {
	if err := platformid.Validate(walletID); err != nil || limit < 1 || limit > 100 {
		return LedgerPage{}, fmt.Errorf("%w: wallet, cursor or limit", ErrInvalidInput)
	}
	var cursor *LedgerCursor
	if opaqueCursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(opaqueCursor)
		if err != nil {
			return LedgerPage{}, fmt.Errorf("%w: cursor", ErrInvalidInput)
		}
		var value LedgerCursor
		if err := json.Unmarshal(decoded, &value); err != nil || value.FormatVersion != ledgerCursorFormatVersion ||
			value.WalletID != walletID || value.WalletVersion < 1 || platformid.Validate(value.EntryID) != nil {
			return LedgerPage{}, fmt.Errorf("%w: cursor", ErrInvalidInput)
		}
		cursor = &value
	}
	entries, err := s.store.ListLedger(ctx, walletID, cursor, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		encoded, err := json.Marshal(LedgerCursor{
			FormatVersion: ledgerCursorFormatVersion, WalletID: walletID,
			WalletVersion: last.WalletVersion(), EntryID: last.ID(),
		})
		if err != nil {
			return LedgerPage{}, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func (s *Service) addOpening(creation *Creation, input OpenInput, now time.Time) error {
	openingID, err := s.newID()
	if err != nil {
		return err
	}
	ledgerID, err := s.newID()
	if err != nil {
		return err
	}
	processedEventID, err := s.newID()
	if err != nil {
		return err
	}
	balanceEventID, err := s.newID()
	if err != nil {
		return err
	}
	zero, err := money.Zero(input.Initial.Currency())
	if err != nil {
		return err
	}
	opening, err := wagering.NewOpening(openingID, creation.Wallet.ID(), input.PlayerID, input.Initial, now)
	if err != nil {
		return err
	}
	entry, err := ledger.New(ledger.Params{
		ID: ledgerID, WalletID: creation.Wallet.ID(), TransactionID: openingID,
		Direction: ledger.DirectionCredit, Amount: input.Initial, BalanceBefore: zero,
		BalanceAfter: input.Initial, WalletVersion: 1, CreatedAt: now,
	})
	if err != nil {
		return err
	}
	processed, err := event.NewWagerTransactionProcessed(event.Metadata{
		EventID: processedEventID, AggregateID: openingID,
		CorrelationID: input.CorrelationID, OccurredAt: now,
	}, event.ProcessedInput{
		TransactionID: openingID, Kind: wagering.KindOpening,
		Money: input.Initial, Balance: input.Initial,
	})
	if err != nil {
		return err
	}
	balanceChanged, err := event.NewWalletBalanceChanged(event.Metadata{
		EventID: balanceEventID, AggregateID: creation.Wallet.ID(),
		CorrelationID: input.CorrelationID, CausationID: processedEventID, OccurredAt: now,
	}, event.WalletBalanceChangedData{
		WalletID: creation.Wallet.ID(), TransactionID: openingID,
		Direction: ledger.DirectionCredit, Money: input.Initial,
		BalanceBefore: zero, BalanceAfter: input.Initial, WalletVersion: 1,
	})
	if err != nil {
		return err
	}
	creation.Opening = opening
	creation.Ledger = entry
	creation.Events = []event.IntegrationEvent{processed, balanceChanged}
	return nil
}
