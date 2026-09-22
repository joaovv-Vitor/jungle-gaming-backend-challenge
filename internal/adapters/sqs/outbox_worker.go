package sqsadapter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

type OutboxWorker struct {
	service *application.Service
	cfg     config.Config
	logger  *slog.Logger
	cancel  context.CancelFunc
	done    sync.WaitGroup
}

func NewOutboxWorker(lifecycle fx.Lifecycle, service *application.Service, cfg config.Config, logger *slog.Logger) *OutboxWorker {
	worker := &OutboxWorker{service: service, cfg: cfg, logger: logger}
	lifecycle.Append(fx.Hook{OnStart: worker.start, OnStop: worker.stop})
	return worker
}

func (w *OutboxWorker) start(ctx context.Context) error {
	if err := w.service.CheckDestination(ctx); err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	for range w.cfg.OutboxWorkers {
		w.done.Add(1)
		go w.run(workerCtx)
	}
	w.logger.Info("outbox publisher started", "workers", w.cfg.OutboxWorkers)
	return nil
}

func (w *OutboxWorker) stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	done := make(chan struct{})
	go func() { w.done.Wait(); close(done) }()
	select {
	case <-done:
		w.logger.Info("outbox publisher stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *OutboxWorker) run(parent context.Context) {
	defer w.done.Done()
	for parent.Err() == nil {
		ctx, cancel := context.WithTimeout(parent, w.cfg.OutboxProcess)
		outcome, err := w.service.ProcessOne(ctx)
		cancel()
		if err != nil && parent.Err() == nil {
			w.logger.Error("outbox publication failed", "error", err)
		}
		if outcome == application.OutcomePublished {
			continue
		}
		timer := time.NewTimer(w.cfg.OutboxPoll)
		select {
		case <-parent.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
