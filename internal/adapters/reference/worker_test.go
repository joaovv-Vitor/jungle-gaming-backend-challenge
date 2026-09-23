package referenceadapter

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/reference"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
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
