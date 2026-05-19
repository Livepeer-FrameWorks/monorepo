package clients

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//nolint:bodyclose // test responses have no body
func TestNewHTTPRetryPolicy_NormalizesConfigToBoundRetries(t *testing.T) {
	cfg := HTTPExecutorConfig{
		MaxRetries: -3,
		BaseDelay:  0,
		MaxDelay:   0,
	}
	policy := NewHTTPRetryPolicy(cfg)

	var attempts int32
	_, err := failsafe.With(policy).Get(func() (*http.Response, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, errors.New("network partition")
	})
	if err == nil {
		t.Fatal("expected request to fail")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("expected bounded single attempt with negative retries, got %d", got)
	}
}

//nolint:bodyclose // test responses have no body
func TestNewHTTPRetryPolicy_RetriesUpToConfiguredLimit(t *testing.T) {
	cfg := HTTPExecutorConfig{
		MaxRetries: 2,
		BaseDelay:  time.Millisecond,
		MaxDelay:   time.Millisecond,
		ShouldRetry: func(_ *http.Response, err error) bool {
			return err != nil
		},
	}
	policy := NewHTTPRetryPolicy(cfg)

	var attempts int32
	_, err := failsafe.With(policy).Get(func() (*http.Response, error) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			return nil, errors.New("dns lag")
		}
		return &http.Response{StatusCode: http.StatusOK}, nil
	})
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("expected exactly 3 attempts (1 + 2 retries), got %d", got)
	}
}

func TestIsRetryableGRPCError_Boundaries(t *testing.T) {
	if !isRetryableGRPCError(status.Error(codes.Unavailable, "connection error: desc = transport: error while dialing")) {
		t.Fatal("expected transport unavailable to be retryable")
	}
	if isRetryableGRPCError(status.Error(codes.Unavailable, "service temporarily unavailable")) {
		t.Fatal("expected downstream unavailable to be non-retryable")
	}
	if !isRetryableGRPCError(status.Error(codes.DeadlineExceeded, "timeout")) {
		t.Fatal("expected deadline exceeded to be retryable")
	}
	if isRetryableGRPCError(status.Error(codes.ResourceExhausted, "tenant storage quota exceeded")) {
		t.Fatal("expected resource exhausted to be non-retryable")
	}
	if isRetryableGRPCError(status.Error(codes.PermissionDenied, "tenant mismatch")) {
		t.Fatal("expected permission denied to be non-retryable")
	}
}

func TestIsCircuitBreakerFailure_Boundaries(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "transport unavailable counts",
			err:  status.Error(codes.Unavailable, "connection error: desc = transport: error while dialing: dial tcp: connect: connection refused"),
			want: true,
		},
		{
			name: "propagated downstream unavailable does not count",
			err:  status.Error(codes.Unavailable, "service temporarily unavailable"),
			want: false,
		},
		{
			name: "internal application error does not count",
			err:  status.Error(codes.Internal, "createClip failed to create clip"),
			want: false,
		},
		{
			name: "resource exhausted domain pressure does not count",
			err:  status.Error(codes.ResourceExhausted, "tenant storage quota exceeded"),
			want: false,
		},
		{
			name: "deadline exceeded counts",
			err:  status.Error(codes.DeadlineExceeded, "deadline exceeded"),
			want: true,
		},
		{
			name: "caller cancellation does not count",
			err:  context.Canceled,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCircuitBreakerFailure(tt.err); got != tt.want {
				t.Fatalf("isCircuitBreakerFailure() = %v, want %v", got, tt.want)
			}
		})
	}
}
