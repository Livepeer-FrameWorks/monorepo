package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// DefaultRetryAttempts and maxRetryDelay bound a replay to about 2.6s of backoff (25ms doubling, capped at 1s), long
// enough to outlast the aborts a multi-statement online migration causes in a colocated YugabyteDB database.
const (
	DefaultRetryAttempts = 8
	maxRetryDelay        = time.Second
)

// IsRetryablePostgresError classifies database errors that are expected to
// succeed when the whole statement or transaction is replayed. Yugabyte can
// surface schema-cache races as SQLSTATE 40001 during rolling deploys.
//
// "cached plan must not change result type" is included as defense-in-depth.
// We default pgx to exec mode (no statement cache; see Connect), so this should
// not occur. If a caller overrides to a cached exec mode, pgx returns this
// error to the application after online DDL (it invalidates the cache for next
// time but does NOT transparently retry), so replaying the statement succeeds.
func IsRetryablePostgresError(err error) bool {
	if err == nil {
		return false
	}
	switch SQLState(err) {
	case "40P01", "40001":
		return true
	}
	// Callers wrap database errors in statuses whose text may omit the cause, so every error in the chain is checked.
	retryable := false
	walkErrorChain(err, func(e error) bool {
		retryable = retryableMessage(strings.ToLower(e.Error()))
		return retryable
	})
	return retryable
}

func retryableMessage(msg string) bool {
	if strings.Contains(msg, "schema version mismatch") ||
		strings.Contains(msg, "catalog version mismatch") ||
		strings.Contains(msg, "mismatched_schema") ||
		strings.Contains(msg, "cached plan must not change result type") ||
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

// walkErrorChain visits err and every error it wraps, including errors.Join branches, until visit returns true.
func walkErrorChain(err error, visit func(error) bool) bool {
	if err == nil {
		return false
	}
	if visit(err) {
		return true
	}
	// The chain is walked by hand so every error in it, not only the outermost, is visited.
	switch wrapped := err.(type) { //nolint:errorlint // this is the chain walk errors.As would hide
	case interface{ Unwrap() []error }:
		for _, inner := range wrapped.Unwrap() {
			if walkErrorChain(inner, visit) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return walkErrorChain(wrapped.Unwrap(), visit)
	}
	return false
}

func retryDelay(baseDelay time.Duration, attempt int) time.Duration {
	delay := baseDelay << attempt
	if delay <= 0 || delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
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
		timer := time.NewTimer(retryDelay(baseDelay, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return err
}

type PostgresTxBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func WithRetryablePostgresTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	return WithRetryablePostgresTxWithHook(ctx, db, opts, nil, fn)
}

func WithRetryablePostgresTxWithHook(ctx context.Context, db *sql.DB, opts *sql.TxOptions, onRetry func(error, int), fn func(*sql.Tx) error) error {
	return WithRetryablePostgresTxBeginnerWithHook(ctx, db, opts, onRetry, fn)
}

// WithRetryablePostgresTxBeginner applies the transaction replay contract to
// database adapters that expose BeginTx without exposing their concrete pool.
func WithRetryablePostgresTxBeginner(ctx context.Context, db PostgresTxBeginner, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	return WithRetryablePostgresTxBeginnerWithHook(ctx, db, opts, nil, fn)
}

func WithRetryablePostgresTxBeginnerWithHook(ctx context.Context, db PostgresTxBeginner, opts *sql.TxOptions, onRetry func(error, int), fn func(*sql.Tx) error) error {
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
	return WithRetryablePostgresRollbackTxWithHook(ctx, db, opts, nil, fn)
}

// WithRetryablePostgresRollbackTxWithHook is WithRetryablePostgresRollbackTx with onRetry called before each replay.
func WithRetryablePostgresRollbackTxWithHook(ctx context.Context, db *sql.DB, opts *sql.TxOptions, onRetry func(error, int), fn func(*sql.Tx) error) error {
	return RetryPostgresWithHook(ctx, DefaultRetryAttempts, 25*time.Millisecond, onRetry, func() error {
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

// bestEffortSavepoint names the savepoint TryInSavepoint uses. Calls do not nest.
const bestEffortSavepoint = "frameworks_best_effort"

// TryInSavepoint runs a statement whose failure the caller tolerates, inside a savepoint on tx. A failed statement
// aborts the whole PostgreSQL transaction, so without the savepoint every later statement would fail with SQLSTATE
// 25P02 and the attempt could not commit. A tolerated failure is rolled back to the savepoint and returned as failed
// for the caller to log. A retryable failure, or a failure to manage the savepoint, is returned as abort: the attempt
// cannot continue and the caller returns it so the retrying transaction replays.
func TryInSavepoint(ctx context.Context, tx *sql.Tx, fn func() error) (failed, abort error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+bestEffortSavepoint); err != nil {
		return nil, err
	}
	if err := fn(); err != nil {
		if IsRetryablePostgresError(err) {
			return nil, err
		}
		if _, rollbackErr := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+bestEffortSavepoint); rollbackErr != nil {
			return nil, errors.Join(err, rollbackErr)
		}
		return err, nil
	}
	if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+bestEffortSavepoint); err != nil {
		return nil, err
	}
	return nil, nil
}
