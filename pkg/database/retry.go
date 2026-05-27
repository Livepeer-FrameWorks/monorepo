package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
)

const DefaultRetryAttempts = 6

// IsRetryablePostgresError classifies database errors that are expected to
// succeed when the whole statement or transaction is replayed. Yugabyte can
// surface schema-cache races as SQLSTATE 40001 during rolling deploys.
func IsRetryablePostgresError(err error) bool {
	if err == nil {
		return false
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch string(pqErr.Code) {
		case "40P01", "40001":
			return true
		}
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "schema version mismatch") ||
		strings.Contains(msg, "catalog version mismatch") ||
		strings.Contains(msg, "mismatched_schema") ||
		strings.Contains(msg, "catalog snapshot") && strings.Contains(msg, "invalidated") {
		return true
	}
	if !strings.Contains(msg, "40001") {
		return false
	}
	return strings.Contains(msg, "read restart") ||
		strings.Contains(msg, "restart read") ||
		strings.Contains(msg, "restart transaction")
}

func RetryPostgres(ctx context.Context, attempts int, baseDelay time.Duration, fn func() error) error {
	return RetryPostgresWithHook(ctx, attempts, baseDelay, nil, fn)
}

func RetryPostgresWithHook(ctx context.Context, attempts int, baseDelay time.Duration, onRetry func(error, int), fn func() error) error {
	if attempts <= 0 {
		attempts = DefaultRetryAttempts
	}
	if baseDelay <= 0 {
		baseDelay = 25 * time.Millisecond
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = fn()
		if !IsRetryablePostgresError(err) || attempt == attempts-1 {
			return err
		}
		if onRetry != nil {
			onRetry(err, attempt+1)
		}
		timer := time.NewTimer(baseDelay << attempt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

func WithRetryablePostgresTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	return WithRetryablePostgresTxWithHook(ctx, db, opts, nil, fn)
}

func WithRetryablePostgresTxWithHook(ctx context.Context, db *sql.DB, opts *sql.TxOptions, onRetry func(error, int), fn func(*sql.Tx) error) error {
	return RetryPostgresWithHook(ctx, DefaultRetryAttempts, 25*time.Millisecond, onRetry, func() error {
		tx, err := db.BeginTx(ctx, opts)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback() //nolint:errcheck // rollback is best-effort when replaying retryable tx failures
			}
		}()
		if err := fn(tx); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		committed = true
		return nil
	})
}

func WithRetryablePostgresRollbackTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	return RetryPostgres(ctx, DefaultRetryAttempts, 25*time.Millisecond, func() error {
		tx, err := db.BeginTx(ctx, opts)
		if err != nil {
			return err
		}
		if err := fn(tx); err != nil {
			_ = tx.Rollback() //nolint:errcheck // rollback-only helper returns the original body error
			return err
		}
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return err
		}
		return nil
	})
}
