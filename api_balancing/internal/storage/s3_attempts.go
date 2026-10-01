package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// S3AttemptPolicy bounds each request of a retried object-store call.
type S3AttemptPolicy struct {
	// MaxAttempts is the number of requests before the call fails; zero or less means one.
	MaxAttempts int
	// AttemptTimeout bounds one request from send to response. A request the object store does not answer in time
	// is abandoned and retried; zero leaves each request bounded only by the caller's context.
	AttemptTimeout time.Duration
	// Backoff is the wait before attempt n+1, multiplied by n.
	Backoff time.Duration
}

// ErrS3AttemptTimedOut marks a request abandoned at its S3AttemptPolicy.AttemptTimeout.
var ErrS3AttemptTimedOut = errors.New("object store did not answer within the attempt timeout")

// S3AttemptFailure describes one failed request that RetryS3 is about to retry.
type S3AttemptFailure struct {
	Attempt     int
	MaxAttempts int
	Elapsed     time.Duration
	Err         error
}

// RetryS3 runs call under policy, giving each attempt its own context bounded by the attempt timeout. A timed-out
// attempt, a transport failure and a 408, 429 or 5xx response are retried; any other provider response (403, 404,
// 412, ...) and the caller's own cancellation end the call at once. onRetry, when set, sees every failed attempt
// that is retried. The returned error names the attempt count and total elapsed time and wraps the last failure.
func RetryS3(ctx context.Context, policy S3AttemptPolicy, call func(ctx context.Context) error, onRetry func(S3AttemptFailure)) error {
	maxAttempts := policy.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	started := time.Now()
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		attemptStarted := time.Now()
		err := runS3Attempt(ctx, policy.AttemptTimeout, call)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == maxAttempts || ctx.Err() != nil || !IsRetryableS3Error(err) {
			break
		}
		if onRetry != nil {
			onRetry(S3AttemptFailure{Attempt: attempt, MaxAttempts: maxAttempts, Elapsed: time.Since(attemptStarted), Err: err})
		}
		if policy.Backoff > 0 {
			timer := time.NewTimer(time.Duration(attempt) * policy.Backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return fmt.Errorf("after %d attempt(s) in %s: %w", attempts, time.Since(started).Round(time.Millisecond), errors.Join(lastErr, ctx.Err()))
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("after %d attempt(s) in %s: %w", attempts, time.Since(started).Round(time.Millisecond), lastErr)
}

// runS3Attempt runs one request, bounded by attemptTimeout when it is positive. A request cut by that bound while
// the caller's context is still live reports ErrS3AttemptTimedOut.
func runS3Attempt(ctx context.Context, attemptTimeout time.Duration, call func(ctx context.Context) error) error {
	if attemptTimeout <= 0 {
		return call(ctx)
	}
	attemptCtx, cancel := context.WithTimeoutCause(ctx, attemptTimeout, ErrS3AttemptTimedOut)
	defer cancel()
	err := call(attemptCtx)
	if err != nil && ctx.Err() == nil && errors.Is(context.Cause(attemptCtx), ErrS3AttemptTimedOut) {
		return fmt.Errorf("%w (%s): %w", ErrS3AttemptTimedOut, attemptTimeout, err)
	}
	return err
}

// IsRetryableS3Error reports whether an object-store failure is worth another attempt: an attempt timeout, a
// transport failure (no HTTP response), or a 408, 429 or 5xx response. A definitive provider answer such as 403,
// 404 or 412 is not, and neither is the caller's own cancellation.
func IsRetryableS3Error(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrS3AttemptTimedOut) {
		return true
	}
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) {
		code := status.HTTPStatusCode()
		if code == 0 {
			return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
		}
		return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}
