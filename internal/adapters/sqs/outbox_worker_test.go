package sqsadapter

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
)

type failingOutboxStore struct{ err error }

func (s failingOutboxStore) Claim(context.Context, time.Duration) (*application.Event, error) {
	return nil, s.err
}
func (failingOutboxStore) Confirm(context.Context, application.Event) error {
	panic("unexpected confirm")
}
func (failingOutboxStore) Retry(context.Context, application.Event, time.Time, string) error {
	panic("unexpected retry")
}
func (failingOutboxStore) Stats(context.Context) (application.Stats, error) {
	return application.Stats{}, nil
}

type idleOutboxPublisher struct{}

func (idleOutboxPublisher) Check(context.Context) error { return nil }
func (idleOutboxPublisher) Publish(context.Context, application.Event) error {
	panic("a failed claim must not publish an event")
}

func TestOutboxWorkerDoesNotLogDependencyErrorDetails(t *testing.T) {
	const secret = "sensitive-outbox-error-sentinel"
	var logs bytes.Buffer
	service := application.NewService(failingOutboxStore{err: errors.New(secret)}, idleOutboxPublisher{},
		config.Config{OutboxLease: time.Second})
	worker := &OutboxWorker{
		service: service, cfg: config.Config{OutboxProcess: time.Second, OutboxPoll: 20 * time.Millisecond},
		logger: slog.New(slog.NewJSONHandler(&logs, nil)), metrics: metrics.New(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	worker.done.Add(1)
	worker.run(ctx)
	if !strings.Contains(logs.String(), "outbox publication failed") || strings.Contains(logs.String(), secret) ||
		!strings.Contains(logs.String(), `"reason":"unexpected"`) {
		t.Fatalf("unsafe outbox worker log: %s", logs.String())
	}
}
