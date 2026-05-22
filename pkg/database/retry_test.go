package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestIsRetryablePostgresError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization", &pq.Error{Code: "40001"}, true},
		{"deadlock", &pq.Error{Code: "40P01"}, true},
		{"schema version text", errors.New("pq: schema version mismatch for table x: expected 31, got 30 (40001)"), true},
		{"wrapped grpc schema version text", errors.New("rpc error: code = Internal desc = database error: pq: schema version mismatch for table x: expected 31, got 30 (40001)"), true},
		{"read restart text", errors.New("pq: restart transaction: read restart required (40001)"), true},
		{"syntax", &pq.Error{Code: "42601"}, false},
		{"non-transient 40001 text", errors.New("application error 40001"), false},
		{"ordinary", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRetryablePostgresError(tt.err); got != tt.want {
				t.Fatalf("IsRetryablePostgresError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryPostgresRetriesThenSucceeds(t *testing.T) {
	attempts := 0
	err := RetryPostgres(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		if attempts < 2 {
			return &pq.Error{Code: "40001"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RetryPostgres returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestRetryPostgresDoesNotRetryPermanentError(t *testing.T) {
	attempts := 0
	wantErr := errors.New("permanent")
	err := RetryPostgres(context.Background(), 3, time.Nanosecond, func() error {
		attempts++
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("RetryPostgres error = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}
