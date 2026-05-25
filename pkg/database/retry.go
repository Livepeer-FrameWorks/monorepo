package database

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lib/pq"
)

const DefaultRetryAttempts = 3

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
	if !strings.Contains(msg, "40001") {
		return false
	}
	return strings.Contains(msg, "schema version mismatch") ||
		strings.Contains(msg, "read restart") ||
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
