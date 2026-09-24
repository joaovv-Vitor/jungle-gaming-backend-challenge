package referenceadapter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	application "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reference"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/safeerror"
)

type Worker struct {
	service *application.Service
	cfg     config.Config
	logger  *slog.Logger
	metrics *metrics.Metrics
	cancel  context.CancelFunc
	done    sync.WaitGroup
}

func NewWorker(lifecycle fx.Lifecycle, service *application.Service, cfg config.Config, logger *slog.Logger, instrumentation *metrics.Metrics) *Worker {
	worker := &Worker{service: service, cfg: cfg, logger: logger, metrics: instrumentation}
	lifecycle.Append(fx.Hook{OnStart: worker.start, OnStop: worker.stop})
	return worker
}

func (w *Worker) start(context.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	for range w.cfg.ReferenceWorkers {
		w.done.Add(1)
		go w.run(ctx)
	}
	w.logger.Info("reference worker started", "workers", w.cfg.ReferenceWorkers)
	return nil
}

func (w *Worker) stop(ctx context.Context) error {
	started := time.Now()
	defer func() { w.metrics.ObserveShutdown("reference", time.Since(started)) }()
	if w.cancel == nil {
		return nil
	}
	w.cancel()
	done := make(chan struct{})
	go func() {
		w.done.Wait()
		close(done)
	}()
	select {
	case <-done:
		w.logger.Info("reference worker stopped")
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) run(parent context.Context) {
	defer w.done.Done()
	for parent.Err() == nil {
		ctx, cancel := context.WithTimeout(parent, w.cfg.ReferenceProcess)
		outcome, claim, err := w.service.ProcessOneWithClaim(ctx)
		cancel()
		logger := w.logger
		if claim != nil {
			logger = logger.With("transactionId", claim.TransactionID, "walletId", claim.WalletID)
		}
		if parent.Err() == nil && outcome != application.OutcomeIdle {
			result := string(outcome)
			if err != nil {
				result = "error"
			}
			w.metrics.RecordReferenceAttempt(result)
		}
		if err != nil && parent.Err() == nil {
			logger.Error("reference processing failed", "reason", safeerror.Reason(err))
		}
		if outcome == application.OutcomeCompleted {
			logger.Info("pending reference completed")
			continue
		}
		timer := time.NewTimer(w.cfg.ReferencePoll)
		select {
		case <-parent.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
