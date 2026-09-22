package bootstrap

import (
	"context"
	"testing"
	"time"

	"go.uber.org/fx"
)

func TestModuleStartsAndStops(t *testing.T) {
	t.Setenv("APP_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("APP_LOG_LEVEL", "info")

	app := fx.New(Module, fx.NopLogger)
	if err := app.Err(); err != nil {
		t.Fatalf("build application: %v", err)
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStart()
	if err := app.Start(startCtx); err != nil {
		t.Fatalf("start application: %v", err)
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStop()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("stop application: %v", err)
	}
}
