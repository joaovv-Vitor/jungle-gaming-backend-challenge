package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/health"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

type server struct {
	http            *http.Server
	status          *health.Status
	logger          *slog.Logger
	metrics         *metrics.Metrics
	shutdownTimeout time.Duration
	listener        net.Listener
}

func newServer(cfg config.Config, mux *http.ServeMux, status *health.Status, logger *slog.Logger, instrumentation *metrics.Metrics) *server {
	return &server{
		http: &http.Server{
			Addr:              cfg.HTTPAddress,
			Handler:           mux,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
		},
		status:          status,
		logger:          logger,
		metrics:         instrumentation,
		shutdownTimeout: cfg.ShutdownTimeout,
	}
}

func registerLifecycle(lifecycle fx.Lifecycle, srv *server) {
	lifecycle.Append(fx.Hook{
		OnStart: srv.start,
		OnStop:  srv.stop,
	})
}

func (s *server) start(_ context.Context) error {
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	s.listener = listener
	s.status.SetReady(true)
	s.logger.Info("http server started", "address", listener.Addr().String())

	go func() {
		if err := s.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.status.SetReady(false)
			s.logger.Error("http server stopped unexpectedly", "error", err)
		}
	}()

	return nil
}

func (s *server) stop(ctx context.Context) error {
	started := time.Now()
	defer func() { s.metrics.ObserveShutdown("http", time.Since(started)) }()
	s.status.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(ctx, s.shutdownTimeout)
	defer cancel()

	err := s.http.Shutdown(shutdownCtx)
	if err != nil {
		return err
	}
	s.logger.Info("http server stopped")
	return nil
}
