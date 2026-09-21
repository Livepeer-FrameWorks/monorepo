package frameworks

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy says when and how long the client waits before sending a
// failed request again.
type RetryPolicy struct {
	// MaxAttempts counts attempts in total, including the first. 1 disables
	// retries.
	MaxAttempts int
	// BaseDelay is the wait before the first retry; each later retry doubles it.
	BaseDelay time.Duration
	// MaxDelay bounds the doubled wait.
	MaxDelay time.Duration
	// MaxRetryAfter: a Retry-After longer than this ends retrying instead of
	// waiting.
	MaxRetryAfter time.Duration
	// Jitter randomizes each wait between half and all of it, so clients do
	// not retry in step.
	Jitter bool
}

// DefaultRetryPolicy is the policy of a client created without one.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts:   3,
	BaseDelay:     250 * time.Millisecond,
	MaxDelay:      4 * time.Second,
	MaxRetryAfter: 30 * time.Second,
	Jitter:        true,
}

// retryableStatus reports the HTTP statuses that are retried: timeouts,
// throttling, and transient server failures.
func retryableStatus(status int) bool {
	switch status {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

// backoffDelay is the wait before retry number retry (1 for the first retry).
func backoffDelay(p RetryPolicy, retry int) time.Duration {
	delay := p.BaseDelay << (retry - 1)
	if delay > p.MaxDelay || delay <= 0 {
		delay = p.MaxDelay
	}
	if !p.Jitter {
		return delay
	}
	half := delay / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// parseRetryAfter reads a Retry-After value, delta seconds or an HTTP date,
// as whole seconds. ok is false when it is missing or unparseable.
func parseRetryAfter(value string, now time.Time) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(value); err == nil && n >= 0 {
		return n, true
	}
	if t, err := http.ParseTime(value); err == nil {
		secs := int((t.Sub(now) + time.Second - 1) / time.Second)
		if secs < 0 {
			secs = 0
		}
		return secs, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
