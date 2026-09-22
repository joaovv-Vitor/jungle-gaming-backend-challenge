package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearConfigEnvironment(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddress != defaultHTTPAddress {
		t.Errorf("HTTPAddress = %q, want %q", cfg.HTTPAddress, defaultHTTPAddress)
	}
	if cfg.ShutdownTimeout != defaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %s, want %s", cfg.ShutdownTimeout, defaultShutdownTimeout)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_HTTP_READ_TIMEOUT", "0s")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_HTTP_ADDR", "127.0.0.1:9090")
	t.Setenv("APP_LOG_LEVEL", "DEBUG")
	t.Setenv("APP_SHUTDOWN_TIMEOUT", "3s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddress != "127.0.0.1:9090" || cfg.LogLevel != "debug" || cfg.ShutdownTimeout != 3*time.Second {
		t.Fatalf("Load() = %+v", cfg)
	}
}

func TestLoadRejectsInvalidDatabasePool(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_DATABASE_MIN_CONNS", "5")
	t.Setenv("APP_DATABASE_MAX_CONNS", "4")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want database pool validation error")
	}
}

func clearConfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("APP_HTTP_ADDR", defaultHTTPAddress)
	t.Setenv("APP_LOG_LEVEL", "info")
	t.Setenv("APP_HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout.String())
	t.Setenv("APP_HTTP_READ_TIMEOUT", defaultReadTimeout.String())
	t.Setenv("APP_HTTP_WRITE_TIMEOUT", defaultWriteTimeout.String())
	t.Setenv("APP_HTTP_IDLE_TIMEOUT", defaultIdleTimeout.String())
	t.Setenv("APP_SHUTDOWN_TIMEOUT", defaultShutdownTimeout.String())
	t.Setenv("APP_DATABASE_URL", defaultDatabaseURL)
	t.Setenv("APP_DATABASE_MAX_CONNS", "20")
	t.Setenv("APP_DATABASE_MIN_CONNS", "2")
	t.Setenv("APP_DATABASE_PING_TIMEOUT", defaultDatabasePing.String())
}
