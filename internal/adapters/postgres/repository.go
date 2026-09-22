package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound        = errors.New("repository entity not found")
	ErrConcurrentWrite = errors.New("repository concurrent write")
	ErrInvalidSchedule = errors.New("invalid pending reference schedule")
)

type ReferenceSchedule struct {
	NextAttemptAt time.Time
	ExpiresAt     time.Time
}

func (s ReferenceSchedule) valid() bool {
	return !s.NextAttemptAt.IsZero() && !s.ExpiresAt.IsZero() && s.ExpiresAt.After(s.NextAttemptAt)
}

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
