package handlers

import (
	"testing"
	"time"
)

func resetBackgroundCleanupSentinel(t *testing.T) {
	t.Helper()
	backgroundCleanupRunning.Store(false)
}

func TestBackgroundCleanupSentinel_SingleRunner(t *testing.T) {
	resetBackgroundCleanupSentinel(t)

	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("first acquisition must succeed when sentinel is idle")
	}
	if backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("second acquisition must fail while one is running")
	}
	backgroundCleanupRunning.Store(false)
	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("third acquisition must succeed after release")
	}
}

func TestAdmissionThresholds_ProjectedUsageDecision(t *testing.T) {
	// Pure-math sanity check of the soft-threshold projection used by
	// admitDefrost: when (used + size) / total > softCleanupThreshold the
	// proactive cleanup tier should fire. This isolates the policy decision
	// from the syscalls that would normally feed it.

	type fixture struct {
		name              string
		total, used, size uint64
		soft              float64
		expectKickoff     bool
	}
	for _, f := range []fixture{
		{"low usage, no kickoff", 1000, 200, 100, 0.85, false},
		{"projected crosses soft", 1000, 700, 200, 0.85, true},
		{"projected exactly at soft", 1000, 700, 150, 0.85, false},
		{"already over soft, kicks off", 1000, 900, 10, 0.85, true},
	} {
		t.Run(f.name, func(t *testing.T) {
			projected := f.used + f.size
			ratio := float64(projected) / float64(f.total)
			got := ratio > f.soft
			if got != f.expectKickoff {
				t.Fatalf("projected=%d ratio=%.3f soft=%.2f: expected kickoff=%v got=%v",
					projected, ratio, f.soft, f.expectKickoff, got)
			}
		})
	}
}

func TestBackgroundCleanupSentinel_GoroutineRelease(t *testing.T) {
	resetBackgroundCleanupSentinel(t)

	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("setup: acquire failed")
	}
	released := make(chan struct{})
	go func() {
		defer close(released)
		// Mimic kickoffBackgroundCleanup's defer release.
		defer backgroundCleanupRunning.Store(false)
		time.Sleep(2 * time.Millisecond)
	}()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("background work did not release sentinel in time")
	}
	if !backgroundCleanupRunning.CompareAndSwap(false, true) {
		t.Fatal("sentinel must be free after goroutine exits")
	}
	backgroundCleanupRunning.Store(false)
}
