package config

import (
	"errors"
	"fmt"
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
	if cfg.DatabaseMaxConns, err = int32Value("APP_DATABASE_MAX_CONNS", cfg.DatabaseMaxConns); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseMinConns, err = int32Value("APP_DATABASE_MIN_CONNS", cfg.DatabaseMinConns); err != nil {
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
