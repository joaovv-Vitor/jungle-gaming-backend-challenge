package reconciliation

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

const testWalletID = "10000000-0000-4000-8000-000000000001"

type snapshotStore struct {
	snapshot Snapshot
	err      error
}

func (s snapshotStore) Snapshot(context.Context, string) (Snapshot, error) { return s.snapshot, s.err }

func TestReconcileReportsSignedDifferenceAndLogsMismatch(t *testing.T) {
	var logs bytes.Buffer
	service := NewService(snapshotStore{snapshot: Snapshot{
		StoredMinor: 7_500, Currency: "BRL", CalculatedMinor: "10000", CheckedEntries: 2,
	}}, metrics.New(), slog.New(slog.NewJSONHandler(&logs, nil)))
	result, err := service.Reconcile(context.Background(), testWalletID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Consistent || result.StoredBalance.String() != "75.00" ||
		result.CalculatedBalance.String() != "100.00" || result.Difference.String() != "-25.00" || result.CheckedEntries != 2 {
		t.Fatalf("reconciliation = %+v", result)
	}
	if !strings.Contains(logs.String(), "wallet reconciliation mismatch") || !strings.Contains(logs.String(), testWalletID) {
		t.Fatalf("mismatch log = %q", logs.String())
	}
}

func TestReconcileRejectsAggregateAndDifferenceOverflow(t *testing.T) {
	for _, calculated := range []string{"9223372036854775808", "-9223372036854775808"} {
		t.Run(calculated, func(t *testing.T) {
			var logs bytes.Buffer
			service := NewService(snapshotStore{snapshot: Snapshot{
				StoredMinor: 1, Currency: "BRL", CalculatedMinor: calculated, CheckedEntries: 1,
			}}, metrics.New(), slog.New(slog.NewJSONHandler(&logs, nil)))
			_, err := service.Reconcile(context.Background(), testWalletID)
			if !errors.Is(err, ErrOverflow) || !strings.Contains(logs.String(), "wallet reconciliation overflow") {
				t.Fatalf("error=%v logs=%q", err, logs.String())
			}
		})
	}
}

func TestReconcilePropagatesMissingWallet(t *testing.T) {
	service := NewService(snapshotStore{err: ErrWalletNotFound}, metrics.New(), slog.Default())
	if _, err := service.Reconcile(context.Background(), testWalletID); !errors.Is(err, ErrWalletNotFound) {
		t.Fatalf("error = %v, want missing wallet", err)
	}
	if _, err := service.Reconcile(context.Background(), "invalid"); !errors.Is(err, ErrInvalidWalletID) {
		t.Fatalf("error = %v, want invalid wallet ID", err)
	}
}
