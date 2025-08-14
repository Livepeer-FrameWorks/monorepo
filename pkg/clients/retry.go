package clients

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"time"
)

// RetryConfig configures retry behavior
type RetryConfig struct {
	MaxRetries     int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	Multiplier     float64
	Jitter         bool
	RetryFunc      func(resp *http.Response, err error) bool
	CircuitBreaker *CircuitBreaker
}

// DefaultRetryConfig returns sensible defaults for HTTP retries
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 3,
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   5 * time.Second,
		Multiplier: 2.0,
		Jitter:     true,
		RetryFunc:  DefaultShouldRetry,
	}
}

// DefaultShouldRetry determines if a request should be retried
func DefaultShouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}

	if resp == nil {
		return true
	}

	// Retry on server errors and rate limits
	switch resp.StatusCode {
	case http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		http.StatusTooManyRequests:
		return true
	default:
		return false
	}
}

// DoWithRetry executes an HTTP request with exponential backoff retry and optional circuit breaker
func DoWithRetry(ctx context.Context, client *http.Client, req *http.Request, config RetryConfig) (*http.Response, error) {
	// If circuit breaker is configured, wrap the call
	if config.CircuitBreaker != nil {
		var resp *http.Response
		var err error

		cbErr := config.CircuitBreaker.Call(func() error {
			resp, err = doRetryAttempts(ctx, client, req, config)

			// For circuit breaker purposes, consider HTTP errors and 5xx status as failures
			if err != nil {
				return err
			}
			if resp != nil && resp.StatusCode >= 500 {
				return fmt.Errorf("server error: %d", resp.StatusCode)
			}
			return nil
		})

		// If circuit breaker failed, return that error
		if cbErr != nil && err == nil {
			return nil, cbErr
		}

		return resp, err
	}

	// No circuit breaker, just do normal retry
	return doRetryAttempts(ctx, client, req, config)
}

// doRetryAttempts handles the actual retry logic
func doRetryAttempts(ctx context.Context, client *http.Client, req *http.Request, config RetryConfig) (*http.Response, error) {
	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			// Calculate delay with exponential backoff
			delay := time.Duration(float64(config.BaseDelay) * math.Pow(config.Multiplier, float64(attempt-1)))
			if delay > config.MaxDelay {
				delay = config.MaxDelay
			}

			// Add jitter to prevent thundering herd
			if config.Jitter {
				jitter := time.Duration(float64(delay) * 0.1 * (2*rand.Float64() - 1))
				delay += jitter
			}

			select {
			case <-ctx.Done():
				return lastResp, ctx.Err()
			case <-time.After(delay):
			}
		}

		// Create a new request for each attempt
		clonedReq := req.Clone(ctx)

		resp, err := client.Do(clonedReq)
		lastResp = resp
		lastErr = err

		// Check if we should retry
		if !config.RetryFunc(resp, err) {
			return resp, err
		}

		// Don't retry on the last attempt
		if attempt == config.MaxRetries {
			break
		}

		// Close response body if we're going to retry
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
	}

	return lastResp, lastErr
}
