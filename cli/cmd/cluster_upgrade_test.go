package cmd

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForHealthRetriesUntilSuccess(t *testing.T) {
	var attempts int32
	check := func() error {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			return errors.New("not ready")
		}
		return nil
	}

	if err := waitForHealth(context.Background(), check, 5*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("expected health check to succeed, got error: %v", err)
	}
}

func TestWaitForHealthTimeout(t *testing.T) {
	errSentinel := errors.New("still failing")
	check := func() error {
		return errSentinel
	}

	err := waitForHealth(context.Background(), check, 5*time.Millisecond, 30*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}
