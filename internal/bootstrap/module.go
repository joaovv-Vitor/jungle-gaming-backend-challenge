package bootstrap

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	authadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/auth"
	httpadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/http"
	postgresadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/postgres"
	referenceadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/reference"
	sqsadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/sqs"
	applicationingestion "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/ingestion"
	applicationoutbox "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/outbox"
	applicationreference "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/reference"
	applicationwagering "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wagering"
	applicationwallet "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/application/wallet"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/metrics"
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
	fx.Module("application", fx.Provide(applicationwallet.NewService, applicationwagering.NewService, applicationingestion.NewService, applicationreference.NewService, applicationoutbox.NewService)),
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
