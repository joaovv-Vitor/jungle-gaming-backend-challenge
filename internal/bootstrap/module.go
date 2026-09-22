package bootstrap

import (
	"log/slog"
	"os"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	httpadapter "github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/adapters/http"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/health"
)

var Module = fx.Options(
	fx.Module("config", fx.Provide(config.Load)),
	fx.Module("logging", fx.Provide(newLogger)),
	fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
		return &fxevent.SlogLogger{Logger: logger}
	}),
	fx.Module("health", fx.Provide(health.New)),
	httpadapter.Module,
)

func newLogger(cfg config.Config) *slog.Logger {
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}

	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
