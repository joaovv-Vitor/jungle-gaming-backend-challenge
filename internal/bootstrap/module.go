package bootstrap

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	authadapter "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/auth"
	httpadapter "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/http"
	postgresadapter "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/postgres"
	referenceadapter "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/reference"
	sqsadapter "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/adapters/sqs"
	applicationingestion "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/ingestion"
	applicationoutbox "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/outbox"
	applicationreconciliation "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reconciliation"
	applicationreference "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/reference"
	applicationwagering "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/application/wallet"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/health"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/metrics"
)

var Module = fx.Options(
	fx.Module("config", fx.Provide(config.Load)),
	fx.Module("logging", fx.Provide(newLogger)),
	fx.Module("metrics", fx.Provide(metrics.New)),
	fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
		return &fxevent.SlogLogger{Logger: logger}
	}),
	fx.Module("health", fx.Provide(health.New)),
	authadapter.Module,
	postgresadapter.Module,
	fx.Module("application", fx.Provide(applicationwallet.NewService, applicationwagering.NewService, applicationingestion.NewService, applicationreference.NewService, applicationoutbox.NewLoggedService, applicationreconciliation.NewService)),
	sqsadapter.Module,
	referenceadapter.Module,
	httpadapter.Module,
)

func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
