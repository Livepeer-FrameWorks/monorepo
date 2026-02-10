package clients

import (
	"context"
	"time"

	"github.com/failsafe-go/failsafe-go/circuitbreaker"
	"github.com/failsafe-go/failsafe-go/failsafegrpc"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"frameworks/pkg/logging"
)

// gRPC retry defaults
const (
	defaultMaxRetries   = 3
	defaultBaseDelay    = 100 * time.Millisecond
	defaultMaxDelay     = 5 * time.Second
	defaultJitterFactor = 0.25 // 25% jitter per AWS/gRPC best practices
)

// isRetryableGRPCError determines if a gRPC error should be retried.
// Retries transient/infrastructure errors only; never retries client or logic errors.
func isRetryableGRPCError(err error) bool {
	if err == nil {
		return false
	}

	switch status.Code(err) {
	case codes.Unavailable,
		codes.DeadlineExceeded,
		codes.ResourceExhausted,
		codes.Aborted:
		return true
	default:
		return false
	}
}

// isCircuitBreakerFailure determines if a gRPC error should count
// as a failure for circuit breaker purposes. Broader than retry:
// includes Internal and Unknown since they indicate server health issues.
func isCircuitBreakerFailure(err error) bool {
	if err == nil {
		return false
	}

	switch status.Code(err) {
	case codes.Internal,
		codes.Unavailable,
		codes.DeadlineExceeded,
		codes.ResourceExhausted,
		codes.Aborted,
		codes.Unknown:
		return true
	default:
		return false
	}
}

// newGRPCRetryPolicy creates a retry policy for gRPC calls with sensible defaults.
func newGRPCRetryPolicy() retrypolicy.RetryPolicy[any] {
	return retrypolicy.NewBuilder[any]().
		WithBackoff(defaultBaseDelay, defaultMaxDelay).
		WithMaxRetries(defaultMaxRetries).
		WithJitterFactor(defaultJitterFactor).
		HandleIf(func(_ any, err error) bool {
			return isRetryableGRPCError(err)
		}).
		Build()
}

// newGRPCCircuitBreaker creates a named circuit breaker with Prometheus metrics.
func newGRPCCircuitBreaker(name string, logger logging.Logger) circuitbreaker.CircuitBreaker[any] {
	cfg := DefaultCircuitBreakerConfig()
	cfg.Name = name
	cfg.Logger = logger
	cfg.OnStateChange = CircuitBreakerMetricsCallback(name)
	return NewGRPCCircuitBreaker[any](cfg)
}

// NewGRPCCircuitBreaker creates a circuit breaker for gRPC calls from config.
func NewGRPCCircuitBreaker[T any](cfg CircuitBreakerConfig) circuitbreaker.CircuitBreaker[T] {
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

// FailsafeUnaryInterceptor returns a gRPC unary client interceptor with retry + circuit breaker.
// Uses failsafegrpc's built-in interceptor for correct per-attempt context propagation.
func FailsafeUnaryInterceptor(serviceName string, logger logging.Logger) grpc.UnaryClientInterceptor {
	retry := newGRPCRetryPolicy()
	cb := newGRPCCircuitBreaker(serviceName, logger)
	return failsafegrpc.NewUnaryClientInterceptor[any](retry, cb)
}

// FailsafeStreamInterceptor returns a gRPC stream client interceptor with circuit breaker.
// Checks CB state before stream creation and records failures from stream establishment.
func FailsafeStreamInterceptor(serviceName string, logger logging.Logger) grpc.StreamClientInterceptor {
	cb := newGRPCCircuitBreaker(serviceName+"-stream", logger)
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		if cb.IsOpen() {
			return nil, status.Errorf(codes.Unavailable, "circuit breaker open: %s", serviceName)
		}

		stream, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil && isCircuitBreakerFailure(err) {
			cb.RecordFailure()
		} else if err == nil {
			cb.RecordSuccess()
		}
		return stream, err
	}
}
