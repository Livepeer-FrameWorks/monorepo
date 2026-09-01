package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestFinalizedTaskRunnerSerializesInitialAndScheduledRuns(t *testing.T) {
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	s := &Scheduler{
		logger:        logging.NewLogger(),
		billingTicker: ticker,
		stopChan:      make(chan struct{}),
		initialDelay:  time.Millisecond,
	}

	var active atomic.Int32
	var maxActive atomic.Int32
	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		s.runFinalizedTasks(func(context.Context) error {
			current := active.Add(1)
			for {
				maximum := maxActive.Load()
				if current <= maximum || maxActive.CompareAndSwap(maximum, current) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			active.Add(-1)
			if runs.Add(1) == 3 {
				close(s.stopChan)
			}
			return nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finalized task runner did not stop")
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("maximum concurrent finalized runs = %d, want 1", got)
	}
}

func TestFinalizedTaskRunnerStopsBeforeDelayedInitialRun(t *testing.T) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	s := &Scheduler{
		logger:        logging.NewLogger(),
		billingTicker: ticker,
		stopChan:      make(chan struct{}),
		initialDelay:  time.Hour,
	}

	var runs atomic.Int32
	done := make(chan struct{})
	go func() {
		s.runFinalizedTasks(func(context.Context) error {
			runs.Add(1)
			return nil
		})
		close(done)
	}()
	close(s.stopChan)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finalized task runner did not stop")
	}
	if got := runs.Load(); got != 0 {
		t.Fatalf("runs after stop = %d, want 0", got)
	}
}
