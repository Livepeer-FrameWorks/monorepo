package database

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lib/pq"
)

func TestRetryPostgres_AttemptsDefaulting(t *testing.T) {
	tests := []struct {
		name      string
		attempts  int
		wantCalls int
	}{
		{"zero_uses_default", 0, DefaultRetryAttempts},
		{"negative_uses_default", -5, DefaultRetryAttempts},
		{"positive_two", 2, 2},
		{"positive_one", 1, 1},
		{"positive_four", 4, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := RetryPostgres(context.Background(), tt.attempts, time.Nanosecond, func() error {
				calls++
				return &pq.Error{Code: "40001"}
			})
			if !IsRetryablePostgresError(err) {
				t.Fatalf("expected final retryable error, got %v", err)
			}
			if calls != tt.wantCalls {
				t.Fatalf("attempts=%d: fn called %d times, want %d", tt.attempts, calls, tt.wantCalls)
			}
		})
	}
}

func TestRetryPostgres_OnRetryFiresExactlyAttemptsMinusOne(t *testing.T) {
	tests := []struct {
		attempts      int
		wantCalls     int
		wantOnRetries int
	}{
		{1, 1, 0},
		{3, 3, 2},
		{5, 5, 4},
	}
	for _, tt := range tests {
		calls := 0
		onRetries := 0
		var lastAttemptArg int
		err := RetryPostgresWithHook(context.Background(), tt.attempts, time.Nanosecond,
			func(_ error, attempt int) {
				onRetries++
				lastAttemptArg = attempt
			},
			func() error {
				calls++
				return &pq.Error{Code: "40001"}
			})
		if !IsRetryablePostgresError(err) {
			t.Fatalf("attempts=%d: expected retryable error, got %v", tt.attempts, err)
		}
		if calls != tt.wantCalls {
			t.Fatalf("attempts=%d: fn called %d times, want %d", tt.attempts, calls, tt.wantCalls)
		}
		if onRetries != tt.wantOnRetries {
			t.Fatalf("attempts=%d: onRetry fired %d times, want %d", tt.attempts, onRetries, tt.wantOnRetries)
		}
		if tt.wantOnRetries > 0 && lastAttemptArg != tt.wantOnRetries {
			t.Fatalf("attempts=%d: last onRetry attempt arg=%d, want %d", tt.attempts, lastAttemptArg, tt.wantOnRetries)
		}
	}
}

func TestRetryPostgres_StopsOnPermanentMidway(t *testing.T) {
	calls := 0
	permanent := errors.New("permanent failure")
	err := RetryPostgres(context.Background(), 6, time.Nanosecond, func() error {
		calls++
		if calls < 3 {
			return &pq.Error{Code: "40001"}
		}
		return permanent
	})
	if !errors.Is(err, permanent) {
		t.Fatalf("expected permanent error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected to stop after 3 calls (2 retryable + 1 permanent), got %d", calls)
	}
}
