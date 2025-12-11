package clients

import (
	"context"
	"errors"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/pkg/logging"
)

// GRPCExecutorConfig configures the gRPC executor
type GRPCExecutorConfig struct {
	// Retry settings
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration

	// Circuit breaker settings
	CircuitBreakerConfig *CircuitBreakerConfig

	// Logger for debugging
	Logger logging.Logger
}

// DefaultGRPCExecutorConfig returns sensible defaults for gRPC
func DefaultGRPCExecutorConfig() GRPCExecutorConfig {
	return GRPCExecutorConfig{
		MaxRetries: 3,
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   5 * time.Second,
	}
}

// isRetryableGRPCError determines if a gRPC error should be retried
func isRetryableGRPCError(err error) bool {
	if err == nil {
		return false
	}

	code := status.Code(err)
	switch code {
	// Retryable errors
	case codes.Unavailable,
		codes.DeadlineExceeded,
		codes.ResourceExhausted,
		codes.Aborted:
		return true

	// Non-retryable errors
	case codes.InvalidArgument,
		codes.NotFound,
		codes.AlreadyExists,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.FailedPrecondition,
		codes.OutOfRange,
		codes.Unimplemented,
		codes.Internal,
		codes.Canceled,
		codes.OK:
		return false

	default:
		// Unknown codes - don't retry to be safe
		return false
	}
}

// isCircuitBreakerFailure determines if a gRPC error should count
// as a failure for circuit breaker purposes
func isCircuitBreakerFailure(err error) bool {
	if err == nil {
		return false
	}

	code := status.Code(err)
	switch code {
	// Server errors - count as failures
	case codes.Internal,
		codes.Unavailable,
		codes.DeadlineExceeded,
		codes.ResourceExhausted,
		codes.Aborted,
		codes.Unknown:
		return true

	// Client errors - don't count as failures (our fault, not server's)
	default:
		return false
	}
}

// NewGRPCRetryPolicy creates a retry policy for gRPC calls
func NewGRPCRetryPolicy[T any](cfg GRPCExecutorConfig) retrypolicy.RetryPolicy[T] {
	return retrypolicy.NewBuilder[T]().
		WithBackoff(cfg.BaseDelay, cfg.MaxDelay).
		WithMaxRetries(cfg.MaxRetries).
		WithJitterFactor(0.1).
		HandleIf(func(_ T, err error) bool {
			return isRetryableGRPCError(err)
		}).
		Build()
}

// NewGRPCCircuitBreaker creates a circuit breaker for gRPC calls
func NewGRPCCircuitBreaker[T any](cfg CircuitBreakerConfig) circuitbreaker.CircuitBreaker[T] {
	// Apply defaults
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

	failureThreshold := uint(float64(cfg.MinRequests) * cfg.FailureRatio)
	if failureThreshold < 1 {
		failureThreshold = 1
	}

	builder := circuitbreaker.NewBuilder[T]().
		WithFailureThresholdRatio(failureThreshold, uint(cfg.MinRequests)).
		WithDelay(cfg.Timeout).
		WithSuccessThreshold(uint(cfg.MaxRequests)).
		HandleIf(func(_ T, err error) bool {
			return isCircuitBreakerFailure(err)
		})

	// Add state change logging
	if cfg.OnStateChange != nil || cfg.Logger != nil {
		builder = builder.OnStateChanged(func(event circuitbreaker.StateChangedEvent) {
			fromState := convertState(event.OldState)
			toState := convertState(event.NewState)

			if cfg.Logger != nil {
				cfg.Logger.WithFields(logging.Fields{
					"circuit_breaker": cfg.Name,
					"from_state":      fromState.String(),
					"to_state":        toState.String(),
				}).Warn("gRPC circuit breaker state change")
			}

			if cfg.OnStateChange != nil {
				cfg.OnStateChange(cfg.Name, fromState, toState)
			}
		})
	}

	return builder.Build()
}

// NewGRPCExecutor creates a failsafe executor for gRPC calls
func NewGRPCExecutor[T any](cfg GRPCExecutorConfig) failsafe.Executor[T] {
	retry := NewGRPCRetryPolicy[T](cfg)

	if cfg.CircuitBreakerConfig != nil {
		cb := NewGRPCCircuitBreaker[T](*cfg.CircuitBreakerConfig)
		return failsafe.With(retry, cb)
	}

	return failsafe.With(retry)
}

// GRPCUnaryClientInterceptor returns a gRPC unary client interceptor
// that applies retry and circuit breaker policies.
func GRPCUnaryClientInterceptor(cfg GRPCExecutorConfig) grpc.UnaryClientInterceptor {
	// Create policies
	retry := retrypolicy.NewBuilder[any]().
		WithBackoff(cfg.BaseDelay, cfg.MaxDelay).
		WithMaxRetries(cfg.MaxRetries).
		WithJitterFactor(0.1).
		HandleIf(func(_ any, err error) bool {
			return isRetryableGRPCError(err)
		}).
		Build()

	var policies []failsafe.Policy[any]
	policies = append(policies, retry)

	if cfg.CircuitBreakerConfig != nil {
		cb := NewGRPCCircuitBreaker[any](*cfg.CircuitBreakerConfig)
		policies = append(policies, cb)
	}

	executor := failsafe.With(policies...)

	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		_, err := executor.WithContext(ctx).Get(func() (any, error) {
			err := invoker(ctx, method, req, reply, cc, opts...)
			return nil, err
		})

		// Convert circuit breaker open error to gRPC Unavailable
		if err != nil && errors.Is(err, circuitbreaker.ErrOpen) {
			return status.Errorf(codes.Unavailable, "circuit breaker open")
		}

		return err
	}
}

// GRPCStreamClientInterceptor returns a gRPC stream client interceptor
// that checks circuit breaker state before creating streams.
func GRPCStreamClientInterceptor(cb *CircuitBreaker) grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		// Check circuit state before attempting stream
		if cb != nil && cb.IsOpen() {
			return nil, status.Errorf(codes.Unavailable, "circuit breaker open: %s", cb.Name())
		}

		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil && cb != nil && isCircuitBreakerFailure(err) {
			// Record failure through circuit breaker
			cb.Call(func() error { return err })
		}
		return stream, err
	}
}

// WithGRPCFailsafe returns gRPC dial options with retry and circuit breaker
func WithGRPCFailsafe(cfg GRPCExecutorConfig) grpc.DialOption {
	return grpc.WithUnaryInterceptor(GRPCUnaryClientInterceptor(cfg))
}
