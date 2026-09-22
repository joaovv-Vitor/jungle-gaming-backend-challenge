package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddress       = ":8080"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 10 * time.Second
	defaultWriteTimeout      = 15 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 15 * time.Second
	defaultDatabaseURL       = "postgres://wager_app:wager_app_local@localhost:5432/wagering?sslmode=disable"
	defaultDatabasePing      = 2 * time.Second
	defaultOIDCIssuer        = "http://localhost:8081/realms/wagering"
	defaultOIDCJWKSURL       = "http://localhost:8081/realms/wagering/protocol/openid-connect/certs"
	defaultOIDCAudience      = "wager-api"
	defaultOIDCPingTimeout   = 2 * time.Second
	defaultSQSEndpoint       = "http://localhost:4566"
	defaultSQSRegion         = "us-east-1"
	defaultSQSInputQueue     = "wager-transactions.fifo"
	defaultSQSConsumerName   = "wager-transactions"
	defaultSQSLongPoll       = 20 * time.Second
	defaultSQSVisibility     = 60 * time.Second
	defaultSQSProcessing     = 20 * time.Second
	defaultSQSShutdown       = 30 * time.Second
	defaultSQSPingTimeout    = 2 * time.Second
)

type Config struct {
	HTTPAddress         string
	LogLevel            string
	ReadHeaderTimeout   time.Duration
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	IdleTimeout         time.Duration
	ShutdownTimeout     time.Duration
	DatabaseURL         string
	DatabaseMaxConns    int32
	DatabaseMinConns    int32
	DatabasePingTimeout time.Duration
	OIDCIssuer          string
	OIDCJWKSURL         string
	OIDCAudience        string
	OIDCPingTimeout     time.Duration
	SQSEndpoint         string
	SQSRegion           string
	SQSAccessKeyID      string
	SQSSecretAccessKey  string
	SQSInputQueue       string
	SQSConsumerName     string
	SQSLongPoll         time.Duration
	SQSVisibility       time.Duration
	SQSProcessing       time.Duration
	SQSShutdown         time.Duration
	SQSPingTimeout      time.Duration
	SQSReceiveBatch     int32
	SQSConcurrency      int32
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddress:         envOrDefault("APP_HTTP_ADDR", defaultHTTPAddress),
		LogLevel:            strings.ToLower(envOrDefault("APP_LOG_LEVEL", "info")),
		ReadHeaderTimeout:   defaultReadHeaderTimeout,
		ReadTimeout:         defaultReadTimeout,
		WriteTimeout:        defaultWriteTimeout,
		IdleTimeout:         defaultIdleTimeout,
		ShutdownTimeout:     defaultShutdownTimeout,
		DatabaseURL:         envOrDefault("APP_DATABASE_URL", defaultDatabaseURL),
		DatabaseMaxConns:    20,
		DatabaseMinConns:    2,
		DatabasePingTimeout: defaultDatabasePing,
		OIDCIssuer:          envOrDefault("APP_OIDC_ISSUER", defaultOIDCIssuer),
		OIDCJWKSURL:         envOrDefault("APP_OIDC_JWKS_URL", defaultOIDCJWKSURL),
		OIDCAudience:        envOrDefault("APP_OIDC_AUDIENCE", defaultOIDCAudience),
		OIDCPingTimeout:     defaultOIDCPingTimeout,
		SQSEndpoint:         envOrDefault("APP_SQS_ENDPOINT", defaultSQSEndpoint),
		SQSRegion:           envOrDefault("APP_SQS_REGION", defaultSQSRegion),
		SQSAccessKeyID:      envOrDefault("APP_SQS_ACCESS_KEY_ID", "test"),
		SQSSecretAccessKey:  envOrDefault("APP_SQS_SECRET_ACCESS_KEY", "test"),
		SQSInputQueue:       envOrDefault("APP_SQS_INPUT_QUEUE", defaultSQSInputQueue),
		SQSConsumerName:     envOrDefault("APP_SQS_CONSUMER_NAME", defaultSQSConsumerName),
		SQSLongPoll:         defaultSQSLongPoll,
		SQSVisibility:       defaultSQSVisibility,
		SQSProcessing:       defaultSQSProcessing,
		SQSShutdown:         defaultSQSShutdown,
		SQSPingTimeout:      defaultSQSPingTimeout,
		SQSReceiveBatch:     10,
		SQSConcurrency:      4,
	}

	var err error
	if cfg.ReadHeaderTimeout, err = duration("APP_HTTP_READ_HEADER_TIMEOUT", cfg.ReadHeaderTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ReadTimeout, err = duration("APP_HTTP_READ_TIMEOUT", cfg.ReadTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WriteTimeout, err = duration("APP_HTTP_WRITE_TIMEOUT", cfg.WriteTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = duration("APP_HTTP_IDLE_TIMEOUT", cfg.IdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration("APP_SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DatabasePingTimeout, err = duration("APP_DATABASE_PING_TIMEOUT", cfg.DatabasePingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.OIDCPingTimeout, err = duration("APP_OIDC_PING_TIMEOUT", cfg.OIDCPingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.SQSLongPoll, err = duration("APP_SQS_LONG_POLL", cfg.SQSLongPoll); err != nil {
		return Config{}, err
	}
	if cfg.SQSVisibility, err = duration("APP_SQS_VISIBILITY_TIMEOUT", cfg.SQSVisibility); err != nil {
		return Config{}, err
	}
	if cfg.SQSProcessing, err = duration("APP_SQS_PROCESSING_TIMEOUT", cfg.SQSProcessing); err != nil {
		return Config{}, err
	}
	if cfg.SQSShutdown, err = duration("APP_SQS_SHUTDOWN_TIMEOUT", cfg.SQSShutdown); err != nil {
		return Config{}, err
	}
	if cfg.SQSPingTimeout, err = duration("APP_SQS_PING_TIMEOUT", cfg.SQSPingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMaxConns, err = int32Value("APP_DATABASE_MAX_CONNS", cfg.DatabaseMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMinConns, err = int32Value("APP_DATABASE_MIN_CONNS", cfg.DatabaseMinConns); err != nil {
		return Config{}, err
	}
	if cfg.SQSReceiveBatch, err = int32Value("APP_SQS_RECEIVE_BATCH", cfg.SQSReceiveBatch); err != nil {
		return Config{}, err
	}
	if cfg.SQSConcurrency, err = int32Value("APP_SQS_CONCURRENCY", cfg.SQSConcurrency); err != nil {
		return Config{}, err
	}

	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if strings.TrimSpace(c.HTTPAddress) == "" {
		return errors.New("APP_HTTP_ADDR must not be empty")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" {
		return fmt.Errorf("APP_LOG_LEVEL must be one of debug or info: %q", c.LogLevel)
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return errors.New("APP_DATABASE_URL must not be empty")
	}
	if c.DatabaseMinConns < 0 || c.DatabaseMaxConns < 1 || c.DatabaseMinConns > c.DatabaseMaxConns {
		return errors.New("database pool sizes must satisfy 0 <= min <= max")
	}
	if err := validHTTPURL("APP_OIDC_ISSUER", c.OIDCIssuer); err != nil {
		return err
	}
	if err := validHTTPURL("APP_OIDC_JWKS_URL", c.OIDCJWKSURL); err != nil {
		return err
	}
	if strings.TrimSpace(c.OIDCAudience) == "" {
		return errors.New("APP_OIDC_AUDIENCE must not be empty")
	}
	if err := validHTTPURL("APP_SQS_ENDPOINT", c.SQSEndpoint); err != nil {
		return err
	}
	if strings.TrimSpace(c.SQSRegion) == "" || strings.TrimSpace(c.SQSAccessKeyID) == "" ||
		strings.TrimSpace(c.SQSSecretAccessKey) == "" || strings.TrimSpace(c.SQSInputQueue) == "" ||
		strings.TrimSpace(c.SQSConsumerName) == "" {
		return errors.New("SQS region, credentials, input queue and consumer name must not be empty")
	}
	if c.SQSLongPoll > 20*time.Second || c.SQSLongPoll%time.Second != 0 {
		return errors.New("APP_SQS_LONG_POLL must be an integral number of seconds no greater than 20s")
	}
	if c.SQSVisibility%time.Second != 0 || c.SQSVisibility <= c.SQSProcessing {
		return errors.New("SQS visibility must use whole seconds and exceed processing timeout")
	}
	if c.SQSReceiveBatch < 1 || c.SQSReceiveBatch > 10 || c.SQSConcurrency < 1 {
		return errors.New("SQS receive batch must be 1..10 and concurrency must be positive")
	}
	return nil
}

func validHTTPURL(name, value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute HTTP URL", name)
	}
	return nil
}

func int32Value(key string, fallback int32) (int32, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return int32(value), nil
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func duration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return value, nil
}
