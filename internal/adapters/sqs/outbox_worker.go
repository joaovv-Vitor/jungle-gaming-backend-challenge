package sqsadapter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/safeerror"
)

type OutboxWorker struct {
	service *application.Service
	cfg     config.Config
	logger  *slog.Logger
	metrics *metrics.Metrics
	cancel  context.CancelFunc
	done    sync.WaitGroup
}

func NewOutboxWorker(lifecycle fx.Lifecycle, service *application.Service, cfg config.Config, logger *slog.Logger, status *health.Status, instrumentation *metrics.Metrics) *OutboxWorker {
	worker := &OutboxWorker{service: service, cfg: cfg, logger: logger, metrics: instrumentation}
	status.Register("outbox_queue", service.CheckDestination)
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
	w.done.Add(1)
	go w.observeBacklog(workerCtx)
	w.logger.Info("outbox publisher started", "workers", w.cfg.OutboxWorkers)
	return nil
}

func (w *OutboxWorker) stop(ctx context.Context) error {
	started := time.Now()
	defer func() { w.metrics.ObserveShutdown("outbox", time.Since(started)) }()
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
			w.logger.Error("outbox publication failed", "reason", safeerror.Reason(err))
		}
		if parent.Err() == nil && outcome != application.OutcomeIdle {
			result := string(outcome)
			if outcome == "" {
				result = "error"
			}
			w.metrics.RecordOutboxAttempt(result)
		}
		if outcome == application.OutcomePublished || outcome == application.OutcomeRepublished {
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

func (w *OutboxWorker) observeBacklog(parent context.Context) {
	defer w.done.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(parent, w.cfg.OutboxProcess)
		stats, err := w.service.Stats(ctx)
		cancel()
		if err == nil {
			w.metrics.SetOutboxBacklog(stats.Pending, stats.OldestAgeSeconds)
		} else if parent.Err() == nil {
			w.logger.Error("outbox backlog query failed", "reason", safeerror.Reason(err))
		}
		select {
		case <-parent.Done():
			return
		case <-ticker.C:
		}
	}
}
