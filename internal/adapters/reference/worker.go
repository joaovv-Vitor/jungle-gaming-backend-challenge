package referenceadapter

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/fx"

	application "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/reference"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

type Worker struct {
	service *application.Service
	cfg     config.Config
	logger  *slog.Logger
	cancel  context.CancelFunc
	done    sync.WaitGroup
}

func NewWorker(lifecycle fx.Lifecycle, service *application.Service, cfg config.Config, logger *slog.Logger) *Worker {
	worker := &Worker{service: service, cfg: cfg, logger: logger}
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
		outcome, err := w.service.ProcessOne(ctx)
		cancel()
		if err != nil && parent.Err() == nil {
			w.logger.Error("reference processing failed", "error", err)
		}
		if outcome == application.OutcomeCompleted {
			w.logger.Info("pending reference completed")
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
