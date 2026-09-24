package referenceadapter

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reference"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

type failingReferenceStore struct{ err error }

func (s failingReferenceStore) Claim(context.Context, time.Duration) (*application.Claim, error) {
	return nil, s.err
}
func (failingReferenceStore) WithinTransaction(context.Context, func(application.Session) error) error {
	panic("a failed claim must not start a financial transaction")
}

func TestReferenceWorkerDoesNotLogDependencyErrorDetails(t *testing.T) {
	const secret = "sensitive-reference-error-sentinel"
	var logs bytes.Buffer
	service := application.NewService(failingReferenceStore{err: errors.New(secret)}, nil,
		config.Config{ReferenceLease: time.Second})
	worker := &Worker{
		service: service, cfg: config.Config{ReferenceProcess: time.Second, ReferencePoll: 20 * time.Millisecond},
		logger: slog.New(slog.NewJSONHandler(&logs, nil)), metrics: metrics.New(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	worker.done.Add(1)
	worker.run(ctx)
	if !strings.Contains(logs.String(), "reference processing failed") || strings.Contains(logs.String(), secret) ||
		!strings.Contains(logs.String(), `"reason":"unexpected"`) {
		t.Fatalf("unsafe reference worker log: %s", logs.String())
	}
}

type claimedReferenceStore struct{ err error }

func (s claimedReferenceStore) Claim(context.Context, time.Duration) (*application.Claim, error) {
	return &application.Claim{TransactionID: "pending-transaction-1", WalletID: "wallet-1", Token: "secret-lease-token"}, nil
}

func (s claimedReferenceStore) WithinTransaction(context.Context, func(application.Session) error) error {
	return s.err
}

func TestReferenceWorkerLogsClaimIdentifiersWithoutLeaseOrError(t *testing.T) {
	const secret = "sensitive-reference-error-sentinel"
	var logs bytes.Buffer
	service := application.NewService(claimedReferenceStore{err: errors.New(secret)}, nil,
		config.Config{ReferenceLease: time.Second})
	worker := &Worker{
		service: service, cfg: config.Config{ReferenceProcess: time.Second, ReferencePoll: 20 * time.Millisecond},
		logger: slog.New(slog.NewJSONHandler(&logs, nil)), metrics: metrics.New(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Millisecond)
	defer cancel()
	worker.done.Add(1)
	worker.run(ctx)
	for _, expected := range []string{`"transactionId":"pending-transaction-1"`, `"walletId":"wallet-1"`} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("missing reference log identifier %s: %s", expected, logs.String())
		}
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "secret-lease-token") {
		t.Fatalf("unsafe reference worker log: %s", logs.String())
	}
}
