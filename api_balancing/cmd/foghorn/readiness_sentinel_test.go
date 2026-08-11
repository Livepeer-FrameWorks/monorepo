package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mediakeys"
)

type fakeSentinelWriter struct {
	failFirst int // fail this many attempts before succeeding
	calls     int
	lastKey   string
	alwaysErr bool
}

func (w *fakeSentinelWriter) PutObject(_ context.Context, key string, _ []byte, _ string) error {
	w.calls++
	w.lastKey = key
	if w.alwaysErr {
		return errors.New("s3 unavailable")
	}
	if w.calls <= w.failFirst {
		return errors.New("transient s3 error")
	}
	return nil
}

// establishReadinessSentinel is CONVERGENT: it retries a transient write failure and returns an error only after the
// whole budget is exhausted, so the caller (Foghorn boot) fails closed rather than leaving Chandler permanently
// unready. It writes the SHARED sentinel key.
func TestEstablishReadinessSentinel(t *testing.T) {
	// Shrink the retry budget so the test is fast; restore after.
	origBackoff, origAttempts := readinessSentinelBackoff, readinessSentinelAttempts
	readinessSentinelBackoff = 0
	readinessSentinelAttempts = 3
	t.Cleanup(func() {
		readinessSentinelBackoff = origBackoff
		readinessSentinelAttempts = origAttempts
	})

	t.Run("succeeds on first attempt and writes the shared key", func(t *testing.T) {
		w := &fakeSentinelWriter{}
		if err := establishReadinessSentinel(context.Background(), w, logging.NewLoggerWithService("test")); err != nil {
			t.Fatalf("expected success, got %v", err)
		}
		if w.calls != 1 {
			t.Fatalf("calls = %d, want 1", w.calls)
		}
		if w.lastKey != mediakeys.ReadinessSentinelKey {
			t.Fatalf("wrote key %q, want %q", w.lastKey, mediakeys.ReadinessSentinelKey)
		}
	})

	t.Run("retries a transient failure until it establishes", func(t *testing.T) {
		w := &fakeSentinelWriter{failFirst: 2}
		if err := establishReadinessSentinel(context.Background(), w, logging.NewLoggerWithService("test")); err != nil {
			t.Fatalf("expected convergence after retries, got %v", err)
		}
		if w.calls != 3 {
			t.Fatalf("calls = %d, want 3 (2 failures + 1 success)", w.calls)
		}
	})

	t.Run("returns an error after the retry budget is exhausted (caller fails closed)", func(t *testing.T) {
		w := &fakeSentinelWriter{alwaysErr: true}
		if err := establishReadinessSentinel(context.Background(), w, logging.NewLoggerWithService("test")); err == nil {
			t.Fatal("a persistent write failure must return an error so the caller fails closed")
		}
		if w.calls != readinessSentinelAttempts {
			t.Fatalf("calls = %d, want %d (full budget)", w.calls, readinessSentinelAttempts)
		}
	})
}
