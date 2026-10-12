package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A pass context ends as soon as its job is stopped, long before its timeout,
// and a nil stop channel leaves only the timeout.
func TestStoppableEndsOnStop(t *testing.T) {
	stop := make(chan struct{})
	ctx, cancel := stoppable(stop, time.Hour)
	defer cancel()
	close(stop)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("pass context outlived Stop")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("pass context ended with %v, want cancellation", ctx.Err())
	}

	timed, cancelTimed := stoppable(nil, 10*time.Millisecond)
	defer cancelTimed()
	<-timed.Done()
	if !errors.Is(timed.Err(), context.DeadlineExceeded) {
		t.Fatalf("pass context without a stop channel ended with %v, want its deadline", timed.Err())
	}

	stopRun := make(chan struct{})
	run, cancelRun := untilStopped(stopRun)
	defer cancelRun()
	close(stopRun)
	select {
	case <-run.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("run context outlived Stop")
	}
}
