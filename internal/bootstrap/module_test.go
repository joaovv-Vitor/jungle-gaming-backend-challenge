package bootstrap

import (
	"context"
	"log/slog"
	"testing"

	"go.uber.org/fx"

	"github.com/joaovv-Vitor/Desafio-Backend-Processamento-Distribu-do-de-Apostas-em-Go/internal/platform/config"
)

func TestModuleDependencyGraph(t *testing.T) {
	t.Setenv("APP_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("APP_LOG_LEVEL", "info")

	if err := fx.ValidateApp(Module, fx.NopLogger); err != nil {
		t.Fatalf("validate application: %v", err)
	}
}

func TestModuleUsesPreloadedConfiguration(t *testing.T) {
	t.Setenv("APP_HTTP_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.HTTPAddress = "127.0.0.1:12345"
	cfg.LogLevel = "debug"
	var provided config.Config
	var logger *slog.Logger
	app := fx.New(Module, fx.Replace(cfg), fx.Populate(&provided, &logger), fx.NopLogger)
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	if provided.HTTPAddress != cfg.HTTPAddress {
		t.Fatalf("provided address = %q, want %q", provided.HTTPAddress, cfg.HTTPAddress)
	}
	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("logging module did not receive preloaded debug configuration")
	}
}
