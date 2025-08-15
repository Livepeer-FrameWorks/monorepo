package clients

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_Transitions(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig())

	// Cause failures to open
	for i := 0; i < cb.failureThreshold; i++ {
		_ = cb.Call(func() error { return errors.New("fail") })
	}
	if cb.State() != StateOpen {
		t.Fatalf("expected OPEN state")
	}

	// Wait for half-open
	time.Sleep(cb.timeout + 10*time.Millisecond)
	_ = cb.Call(func() error { return nil })
	if cb.State() != StateHalfOpen && cb.State() != StateClosed {
		t.Fatalf("expected HALF-OPEN or CLOSED state")
	}
}
