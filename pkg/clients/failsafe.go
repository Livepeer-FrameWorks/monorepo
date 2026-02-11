package clients

import (
	"context"
	"net/http"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/retrypolicy"

	"frameworks/pkg/logging"
)

// CircuitBreakerState represents the state of the circuit breaker.
type CircuitBreakerState int

const (
	StateClosed CircuitBreakerState = iota
	StateHalfOpen
	StateOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig configures the circuit breaker.
type CircuitBreakerConfig struct {
	// Name identifies this circuit breaker in logs and metrics
	Name string

	// MaxRequests is the number of successful requests needed in half-open
	// state before transitioning to closed. Default: 1
	MaxRequests uint32

	// Timeout is the duration the circuit stays open before transitioning
	// to half-open. Default: 15 seconds.
	Timeout time.Duration

	// FailureRatio is the threshold at which the circuit trips. When the ratio
	// of failures to total requests exceeds this value, the circuit opens.
	// Default: 0.5 (50%)
	FailureRatio float64

	// MinRequests is the minimum number of requests needed before the failure
	// ratio is evaluated. This prevents tripping on small sample sizes.
	// Default: 10
	MinRequests uint32

	// Logger for state change notifications
	Logger logging.Logger

	// OnStateChange is an optional callback invoked when the circuit breaker
	// changes state.
	OnStateChange func(name string, from, to CircuitBreakerState)
}

// DefaultCircuitBreakerConfig returns sensible defaults for the circuit breaker.
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		Name:         "default",
		MaxRequests:  1,
		Timeout:      15 * time.Second,
		FailureRatio: 0.5,
		MinRequests:  10,
	}
}

// CircuitBreaker wraps failsafe-go's circuit breaker with our config interface.
type CircuitBreaker struct {
	cb     circuitbreaker.CircuitBreaker[any]
	name   string
	logger logging.Logger
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration.
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	// Apply defaults
	if cfg.Name == "" {
		cfg.Name = "circuit-breaker"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.FailureRatio == 0 {
		cfg.FailureRatio = 0.5
	}
	if cfg.MinRequests == 0 {
		cfg.MinRequests = 10
	}
	if cfg.MaxRequests == 0 {
		cfg.MaxRequests = 1
	}

	// Calculate failure threshold from ratio
	// e.g., 50% of 10 requests = 5 failures
	failureThreshold := uint(float64(cfg.MinRequests) * cfg.FailureRatio)
	if failureThreshold < 1 {
		failureThreshold = 1
	}

	builder := circuitbreaker.NewBuilder[any]().
		WithFailureThresholdRatio(failureThreshold, uint(cfg.MinRequests)).
		WithDelay(cfg.Timeout).
		WithSuccessThreshold(uint(cfg.MaxRequests))

	// Add state change callback
	if cfg.OnStateChange != nil || cfg.Logger != nil {
		builder = builder.OnStateChanged(func(event circuitbreaker.StateChangedEvent) {
			fromState := convertState(event.OldState)
			toState := convertState(event.NewState)

			if cfg.Logger != nil {
				cfg.Logger.WithFields(logging.Fields{
					"circuit_breaker": cfg.Name,
					"from_state":      fromState.String(),
					"to_state":        toState.String(),
				}).Warn("circuit breaker state change")
			}

			if cfg.OnStateChange != nil {
				cfg.OnStateChange(cfg.Name, fromState, toState)
			}
		})
	}

	return &CircuitBreaker{
		cb:     builder.Build(),
		name:   cfg.Name,
		logger: cfg.Logger,
	}
}

// convertState converts failsafe-go state to our state type
func convertState(state circuitbreaker.State) CircuitBreakerState {
	switch state {
	case circuitbreaker.ClosedState:
		return StateClosed
	case circuitbreaker.HalfOpenState:
		return StateHalfOpen
	case circuitbreaker.OpenState:
		return StateOpen
	default:
		return StateClosed
	}
}

// Call executes the given function through the circuit breaker.
func (cb *CircuitBreaker) Call(fn func() error) error {
	_, err := failsafe.With(cb.cb).Get(func() (any, error) {
		return nil, fn()
	})
	return err
}

// Execute runs a function that returns a value through the circuit breaker.
func (cb *CircuitBreaker) Execute(fn func() (any, error)) (any, error) {
	return failsafe.With(cb.cb).Get(fn)
}

// State returns the current state of the circuit breaker.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	return convertState(cb.cb.State())
}

// Name returns the name of the circuit breaker.
func (cb *CircuitBreaker) Name() string {
	return cb.name
}

// IsOpen returns true if the circuit breaker is open
func (cb *CircuitBreaker) IsOpen() bool {
	return cb.cb.IsOpen()
}

// IsClosed returns true if the circuit breaker is closed
func (cb *CircuitBreaker) IsClosed() bool {
	return cb.cb.IsClosed()
}

// Underlying returns the underlying failsafe-go circuit breaker
// for advanced use cases (e.g., gRPC interceptors)
func (cb *CircuitBreaker) Underlying() circuitbreaker.CircuitBreaker[any] {
	return cb.cb
}

// ============================================================================
// HTTP Executor with Retry + Circuit Breaker
// ============================================================================

// DefaultShouldRetry determines if an HTTP request should be retried.
// Retries on network errors, server errors (5xx), and rate limits (429).
func DefaultShouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return true
	}
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

// HTTPExecutorConfig configures the HTTP executor
type HTTPExecutorConfig struct {
	// Retry settings
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration

	// Circuit breaker (optional)
	CircuitBreaker *CircuitBreaker

	// ShouldRetry determines if a response should trigger a retry
	ShouldRetry func(resp *http.Response, err error) bool
}

// DefaultHTTPExecutorConfig returns sensible defaults
func DefaultHTTPExecutorConfig() HTTPExecutorConfig {
	return HTTPExecutorConfig{
		MaxRetries:  3,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		ShouldRetry: DefaultShouldRetry,
	}
}

func normalizeHTTPExecutorConfig(cfg HTTPExecutorConfig) HTTPExecutorConfig {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 100 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 5 * time.Second
	}
	if cfg.MaxDelay < cfg.BaseDelay {
		cfg.MaxDelay = cfg.BaseDelay
	}
	if cfg.ShouldRetry == nil {
		cfg.ShouldRetry = DefaultShouldRetry
	}
	return cfg
}

// NewHTTPRetryPolicy creates a retry policy for HTTP requests
//
//nolint:bodyclose // false positive: [*http.Response] is a generic type parameter, not an actual response
func NewHTTPRetryPolicy(cfg HTTPExecutorConfig) retrypolicy.RetryPolicy[*http.Response] {
	cfg = normalizeHTTPExecutorConfig(cfg)
	builder := retrypolicy.NewBuilder[*http.Response]().
		WithBackoff(cfg.BaseDelay, cfg.MaxDelay).
		WithMaxRetries(cfg.MaxRetries).
		WithJitterFactor(0.1) // 10% jitter

	// Add retry condition based on response
	if cfg.ShouldRetry != nil {
		builder = builder.HandleIf(func(resp *http.Response, err error) bool {
			return cfg.ShouldRetry(resp, err)
		})
	}

	return builder.Build()
}

// NewHTTPExecutor creates a failsafe executor for HTTP requests
// combining retry policy and optional circuit breaker
//
//nolint:bodyclose // false positive: [*http.Response] is a generic type parameter, not an actual response
func NewHTTPExecutor(cfg HTTPExecutorConfig) failsafe.Executor[*http.Response] {
	retry := NewHTTPRetryPolicy(cfg)

	if cfg.CircuitBreaker != nil {
		// Create a typed circuit breaker for HTTP responses
		httpCB := circuitbreaker.NewBuilder[*http.Response]().
			WithFailureThresholdRatio(5, 10).
			WithDelay(15 * time.Second).
			WithSuccessThreshold(1).
			HandleIf(func(resp *http.Response, err error) bool {
				// Count as failure if error or 5xx status
				if err != nil {
					return true
				}
				if resp != nil && resp.StatusCode >= 500 {
					return true
				}
				return false
			}).
			Build()

		return failsafe.With(retry, httpCB)
	}

	return failsafe.With(retry)
}

// ExecuteHTTP runs an HTTP request through the executor
func ExecuteHTTP(ctx context.Context, executor failsafe.Executor[*http.Response], fn func() (*http.Response, error)) (*http.Response, error) {
	return executor.WithContext(ctx).Get(fn)
}
