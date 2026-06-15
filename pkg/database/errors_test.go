package database

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/yugabyte/pgx/v5/pgconn"
)

func TestSQLState(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", errors.New("boom"), ""},
		{"pgx PgError", &pgconn.PgError{Code: "40001"}, "40001"},
		{"libpq Error", &pq.Error{Code: "23505"}, "23505"},
		{"wrapped pgx", fmt.Errorf("tx: %w", &pgconn.PgError{Code: "40P01"}), "40P01"},
		{"wrapped libpq", fmt.Errorf("tx: %w", &pq.Error{Code: "23503"}), "23503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SQLState(tt.err); got != tt.want {
				t.Fatalf("SQLState(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestIsRetryablePostgresErrorAcrossDrivers(t *testing.T) {
	// The retry classifier must recognize serialization/deadlock from either
	// driver's error type after the lib/pq -> pgx swap.
	retryable := []error{
		&pgconn.PgError{Code: "40001"},
		&pgconn.PgError{Code: "40P01"},
		&pq.Error{Code: "40001"},
		// Cached-plan errors from a cached pgx mode are replay-safe.
		errors.New("ERROR: cached plan must not change result type (SQLSTATE 0A000)"),
	}
	for _, err := range retryable {
		if !IsRetryablePostgresError(err) {
			t.Errorf("expected retryable: %v", err)
		}
	}
	if IsRetryablePostgresError(&pgconn.PgError{Code: "23505"}) {
		t.Error("unique violation must not be retryable")
	}
}
