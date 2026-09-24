package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/config"
	"github.com/joaovv-Vitor/jungle-gaming-backend-challenge/internal/platform/health"
)

func NewPool(lifecycle fx.Lifecycle, cfg config.Config, status *health.Status, logger *slog.Logger) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = cfg.DatabaseMaxConns
	poolConfig.MinConns = cfg.DatabaseMinConns

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}

	ping := func(parent context.Context) error {
		ctx, cancel := context.WithTimeout(parent, cfg.DatabasePingTimeout)
		defer cancel()
		return pool.Ping(ctx)
	}
	status.Register("postgres", ping)
	lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := ping(ctx); err != nil {
				pool.Close()
				return fmt.Errorf("ping database: %w", err)
			}
			logger.Info("database pool started", "maxConnections", cfg.DatabaseMaxConns, "minConnections", cfg.DatabaseMinConns)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			logger.Info("database pool stopped")
			return nil
		},
	})
	return pool, nil
}
