package reconciliation

import (
	"context"
	"errors"
	"log/slog"
	"math/big"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/domain/money"
	platformid "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/id"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

var (
	ErrInvalidWalletID = errors.New("invalid wallet id")
	ErrWalletNotFound  = errors.New("wallet not found")
	ErrOverflow        = errors.New("reconciliation overflow")
	ErrInvalidSnapshot = errors.New("invalid reconciliation snapshot")
)

type Snapshot struct {
	StoredMinor     int64
	Currency        string
	CalculatedMinor string
	CheckedEntries  int64
}

type Store interface {
	Snapshot(context.Context, string) (Snapshot, error)
}

type Result struct {
	WalletID          string      `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

type Service struct {
	store   Store
	metrics *metrics.Metrics
	logger  *slog.Logger
}

func NewService(store Store, instrumentation *metrics.Metrics, logger *slog.Logger) *Service {
	return &Service{store: store, metrics: instrumentation, logger: logger}
}

func (s *Service) Reconcile(ctx context.Context, walletID string) (Result, error) {
	if platformid.Validate(walletID) != nil {
		return Result{}, ErrInvalidWalletID
	}
	snapshot, err := s.store.Snapshot(ctx, walletID)
	if err != nil {
		if !errors.Is(err, ErrWalletNotFound) {
			s.metrics.RecordReconciliation("error")
		}
		return Result{}, err
	}
	currency, err := money.ParseCurrency(snapshot.Currency)
	if err != nil || snapshot.CheckedEntries < 0 || snapshot.StoredMinor < 0 {
		s.metrics.RecordReconciliation("error")
		s.logger.Error("invalid wallet reconciliation snapshot", "walletId", walletID)
		return Result{}, ErrInvalidSnapshot
	}
	calculated, ok := new(big.Int).SetString(snapshot.CalculatedMinor, 10)
	if !ok || !calculated.IsInt64() {
		return Result{}, s.overflow(walletID)
	}
	difference := new(big.Int).Sub(big.NewInt(snapshot.StoredMinor), calculated)
	if !difference.IsInt64() {
		return Result{}, s.overflow(walletID)
	}
	storedMoney, _ := money.New(snapshot.StoredMinor, currency)
	calculatedMoney, _ := money.New(calculated.Int64(), currency)
	differenceMoney, _ := money.New(difference.Int64(), currency)
	result := Result{
		WalletID: walletID, StoredBalance: storedMoney, CalculatedBalance: calculatedMoney,
		Difference: differenceMoney, Consistent: difference.Sign() == 0,
		CheckedEntries: snapshot.CheckedEntries,
	}
	if result.Consistent {
		s.metrics.RecordReconciliation("consistent")
	} else {
		s.metrics.RecordReconciliation("mismatch")
		s.logger.Warn("wallet reconciliation mismatch", "walletId", walletID,
			"difference", differenceMoney.String(), "checkedEntries", snapshot.CheckedEntries)
	}
	return result, nil
}

func (s *Service) overflow(walletID string) error {
	s.metrics.RecordReconciliation("overflow")
	s.logger.Error("wallet reconciliation overflow", "walletId", walletID)
	return ErrOverflow
}
