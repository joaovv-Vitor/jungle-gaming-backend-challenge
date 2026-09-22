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

func TestLoadRejectsInvalidOIDCConfiguration(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_OIDC_ISSUER", "keycloak/realms/wagering")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want OIDC URL validation error")
	}
}

func TestLoadRejectsIncompatibleSQSConfiguration(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_SQS_VISIBILITY_TIMEOUT", "20s")
	t.Setenv("APP_SQS_PROCESSING_TIMEOUT", "20s")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want SQS timing validation error")
	}
}

func TestLoadRejectsReferenceLeaseShorterThanProcessing(t *testing.T) {
	clearConfigEnvironment(t)
	t.Setenv("APP_REFERENCE_LEASE", "5s")
	t.Setenv("APP_REFERENCE_PROCESSING_TIMEOUT", "10s")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a reference lease shorter than processing timeout")
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
	t.Setenv("APP_OIDC_ISSUER", defaultOIDCIssuer)
	t.Setenv("APP_OIDC_JWKS_URL", defaultOIDCJWKSURL)
	t.Setenv("APP_OIDC_AUDIENCE", defaultOIDCAudience)
	t.Setenv("APP_OIDC_PING_TIMEOUT", defaultOIDCPingTimeout.String())
	t.Setenv("APP_SQS_ENDPOINT", defaultSQSEndpoint)
	t.Setenv("APP_SQS_REGION", defaultSQSRegion)
	t.Setenv("APP_SQS_ACCESS_KEY_ID", "test")
	t.Setenv("APP_SQS_SECRET_ACCESS_KEY", "test")
	t.Setenv("APP_SQS_INPUT_QUEUE", defaultSQSInputQueue)
	t.Setenv("APP_SQS_CONSUMER_NAME", defaultSQSConsumerName)
	t.Setenv("APP_SQS_LONG_POLL", defaultSQSLongPoll.String())
	t.Setenv("APP_SQS_VISIBILITY_TIMEOUT", defaultSQSVisibility.String())
	t.Setenv("APP_SQS_PROCESSING_TIMEOUT", defaultSQSProcessing.String())
	t.Setenv("APP_SQS_SHUTDOWN_TIMEOUT", defaultSQSShutdown.String())
	t.Setenv("APP_SQS_PING_TIMEOUT", defaultSQSPingTimeout.String())
	t.Setenv("APP_SQS_RECEIVE_BATCH", "10")
	t.Setenv("APP_SQS_CONCURRENCY", "4")
	t.Setenv("APP_REFERENCE_POLL_INTERVAL", defaultReferencePoll.String())
	t.Setenv("APP_REFERENCE_LEASE", defaultReferenceLease.String())
	t.Setenv("APP_REFERENCE_PROCESSING_TIMEOUT", defaultReferenceProcess.String())
	t.Setenv("APP_REFERENCE_WORKERS", "2")
}
