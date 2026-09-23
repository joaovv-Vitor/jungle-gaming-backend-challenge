package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TransactionWork func(context.Context, pgx.Tx) error

type UnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) *UnitOfWork {
	return &UnitOfWork{pool: pool}
}

func (u *UnitOfWork) ReadCommitted(ctx context.Context, work TransactionWork) error {
	return u.within(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, work)
}

// ReadCommittedWithRetry repeats the complete database transaction only when
// PostgreSQL definitively aborted it due to a deadlock or serialization error.
// Callers must keep external I/O outside work and rebuild attempt-local output.
func (u *UnitOfWork) ReadCommittedWithRetry(ctx context.Context, work TransactionWork) error {
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err := u.ReadCommitted(ctx, work)
		if err == nil || !retryableTransactionAbort(err) || attempt == maxAttempts {
			return err
		}
		delay := time.Duration(1<<(attempt-1))*20*time.Millisecond + time.Duration(rand.Int63n(int64(20*time.Millisecond)))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func retryableTransactionAbort(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) &&
		(databaseError.Code == "40P01" || databaseError.Code == "40001")
}

func (u *UnitOfWork) RepeatableReadOnly(ctx context.Context, work TransactionWork) error {
	return u.within(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, work)
}

func (u *UnitOfWork) within(ctx context.Context, options pgx.TxOptions, work TransactionWork) (err error) {
	if work == nil {
		return errors.New("transaction work is required")
	}
	tx, err := u.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		rollbackErr := tx.Rollback(rollbackCtx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback transaction: %w", rollbackErr))
		}
	}()

	if err := work(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
