package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A restart must not leave storage usage without a sample for a full hour.
func TestStorageSnapshotScheduleRunsSoonAfterStartup(t *testing.T) {
	if storageSnapshotInitialDelay >= storageSnapshotInterval {
		t.Fatalf("initial delay %v must be shorter than the interval %v", storageSnapshotInitialDelay, storageSnapshotInterval)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	ran := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runStorageSnapshotSchedule(ctx, 10*time.Millisecond, time.Hour, func() error {
			calls.Add(1)
			ran <- struct{}{}
			return nil
		}, logging.NewLogger())
	}()

	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("no storage snapshot after the initial delay")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("schedule did not stop on context cancellation")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("snapshot runs = %d, want exactly 1 before the first hourly tick", got)
	}
}

func TestStorageSnapshotScheduleStopsBeforeInitialRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	runStorageSnapshotSchedule(ctx, time.Hour, time.Hour, func() error {
		called = true
		return nil
	}, logging.NewLogger())
	if called {
		t.Fatal("snapshot ran after the context was cancelled")
	}
}
