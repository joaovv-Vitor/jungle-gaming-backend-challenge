package bootstrap

import (
	"testing"

	"go.uber.org/fx"
)

func TestModuleDependencyGraph(t *testing.T) {
	t.Setenv("APP_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("APP_LOG_LEVEL", "info")

	if err := fx.ValidateApp(Module, fx.NopLogger); err != nil {
		t.Fatalf("validate application: %v", err)
	}
}
