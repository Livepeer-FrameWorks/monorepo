package clients

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	fsCircuitbreaker "github.com/failsafe-go/failsafe-go/circuitbreaker"
)

func TestCircuitBreaker_StartsInClosedState(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	if cb.State() != StateClosed {
		t.Fatalf("expected circuit breaker to start in CLOSED state, got %s", cb.State().String())
	}
}

func TestCircuitBreaker_DoesNotTripBelowFailureThreshold(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Name:         "test-below-threshold",
		MinRequests:  10,  // Need 5 failures out of 10 requests (50%)
		FailureRatio: 0.5, // 50% failure rate
		Timeout:      100 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	// Mix of successes and failures that stays below 50% failure rate
	// 4 failures + 6 successes = 40% failure rate (below 50% threshold)
	for i := 0; i < 4; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}
	for i := 0; i < 6; i++ {
		_ = cb.Call(func() error { return nil })
	}

	// Should still be CLOSED because failure ratio is below threshold
	if cb.State() != StateClosed {
		t.Fatalf("expected CLOSED state when below failure threshold, got %s", cb.State().String())
	}
}

func TestCircuitBreaker_TripsWhenFailureRatioExceeded(t *testing.T) {
	var stateChanges []string
	cfg := CircuitBreakerConfig{
		Name:         "test-trip",
		MinRequests:  5,   // Lower threshold for testing
		FailureRatio: 0.5, // Trip at 50% failures
		Timeout:      100 * time.Millisecond,
		OnStateChange: func(name string, from, to CircuitBreakerState) {
			stateChanges = append(stateChanges, to.String())
		},
	}
	cb := NewCircuitBreaker(cfg)

	// Cause enough failures to exceed ratio (need >= 50% failures with >= 5 requests)
	// 5 failures out of 5 requests = 100% failure rate
	for i := 0; i < 5; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN state after failure ratio exceeded, got %s", cb.State().String())
	}

	// Verify state change callback was called
	if len(stateChanges) == 0 {
		t.Fatal("expected OnStateChange callback to be called")
	}
	if stateChanges[0] != "open" {
		t.Fatalf("expected state change to 'open', got %s", stateChanges[0])
	}
}

func TestCircuitBreaker_RejectsCallsWhenOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Name:         "test-reject",
		MinRequests:  3,
		FailureRatio: 0.5,
		Timeout:      1 * time.Second, // Long timeout to keep it open
	}
	cb := NewCircuitBreaker(cfg)

	// Trip the circuit
	for i := 0; i < 3; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN state, got %s", cb.State().String())
	}

	// Subsequent calls should be rejected immediately
	err := cb.Call(func() error { return nil })
	if err == nil {
		t.Fatal("expected error when circuit is open")
	}
	if !errors.Is(err, fsCircuitbreaker.ErrOpen) {
		t.Fatalf("expected circuit breaker open error, got %v", err)
	}
}

func TestCircuitBreaker_TransitionsToHalfOpen(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Name:         "test-half-open",
		MinRequests:  3,
		FailureRatio: 0.5,
		Timeout:      50 * time.Millisecond, // Short timeout for testing
	}
	cb := NewCircuitBreaker(cfg)

	// Trip the circuit
	for i := 0; i < 3; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN state, got %s", cb.State().String())
	}

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	// Next call should be allowed (half-open probe)
	err := cb.Call(func() error { return nil })
	if err != nil {
		t.Fatalf("expected call to succeed in half-open, got %v", err)
	}

	// After successful call in half-open, should transition to closed
	if cb.State() != StateClosed {
		t.Fatalf("expected CLOSED state after successful half-open call, got %s", cb.State().String())
	}
}

func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Name:         "test-half-open-fail",
		MinRequests:  3,
		FailureRatio: 0.5,
		Timeout:      50 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	// Trip the circuit
	for i := 0; i < 3; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}

	// Wait for timeout to enter half-open
	time.Sleep(60 * time.Millisecond)

	// Fail in half-open state
	_ = cb.Call(func() error { return errors.New("fail again") })

	// Should be back to open
	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN state after failure in half-open, got %s", cb.State().String())
	}
}

func TestCircuitBreaker_Execute(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	// Test successful execution with return value
	result, err := cb.Execute(func() (any, error) {
		return "success", nil
	})

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if result != "success" {
		t.Fatalf("expected 'success', got %v", result)
	}
}

func TestCircuitBreaker_Name(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Name: "my-circuit",
	}
	cb := NewCircuitBreaker(cfg)

	if cb.Name() != "my-circuit" {
		t.Fatalf("expected name 'my-circuit', got %s", cb.Name())
	}
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cfg := CircuitBreakerConfig{
		Name:         "test-concurrent",
		MinRequests:  1000, // High threshold to avoid tripping
		FailureRatio: 0.5,
		Timeout:      100 * time.Millisecond,
	}
	cb := NewCircuitBreaker(cfg)

	var successCount int64
	done := make(chan bool, 100)

	// Launch 100 concurrent goroutines
	for i := 0; i < 100; i++ {
		go func() {
			err := cb.Call(func() error { return nil })
			if err == nil {
				atomic.AddInt64(&successCount, 1)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}

	if successCount != 100 {
		t.Fatalf("expected 100 successful calls, got %d", successCount)
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()

	if cfg.Name != "default" {
		t.Errorf("expected name 'default', got %s", cfg.Name)
	}
	if cfg.MaxRequests != 1 {
		t.Errorf("expected MaxRequests 1, got %d", cfg.MaxRequests)
	}
	if cfg.Timeout != 15*time.Second {
		t.Errorf("expected Timeout 15s, got %v", cfg.Timeout)
	}
	if cfg.FailureRatio != 0.5 {
		t.Errorf("expected FailureRatio 0.5, got %v", cfg.FailureRatio)
	}
	if cfg.MinRequests != 10 {
		t.Errorf("expected MinRequests 10, got %d", cfg.MinRequests)
	}
}
