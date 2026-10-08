package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSweepCoalescerQueuedCallersGetAFreshSweep proves a caller that arrives
// while a sweep runs is answered by a sweep that started after it arrived, so
// the change that woke it is published, and that every such caller shares that
// one queued sweep.
func TestSweepCoalescerQueuedCallersGetAFreshSweep(t *testing.T) {
	var c sweepCoalescer
	var sweeps atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	sweep := func(context.Context) (map[string]string, error) {
		n := sweeps.Add(1)
		started <- struct{}{}
		if n == 1 {
			<-release
		}
		return map[string]string{"sweep": string(rune('0' + n))}, nil
	}

	ctx := context.Background()
	first := make(chan map[string]string, 1)
	go func() {
		res, _ := c.do(ctx, "livepeer-gateway", sweep)
		first <- res
	}()
	<-started

	// Three callers arrive while sweep 1 runs; their change may postdate its
	// listing.
	late := make(chan map[string]string, 3)
	var joined sync.WaitGroup
	var entered atomic.Int32
	for range 3 {
		joined.Go(func() {
			entered.Add(1)
			res, _ := c.do(ctx, "livepeer-gateway", sweep)
			late <- res
		})
	}
	waitFor(t, func() bool { return entered.Load() == 3 })
	time.Sleep(50 * time.Millisecond)
	close(release)
	joined.Wait()

	if got := (<-first)["sweep"]; got != "1" {
		t.Fatalf("first caller got sweep %s, want 1", got)
	}
	for range 3 {
		if got := (<-late)["sweep"]; got != "2" {
			t.Fatalf("a caller that arrived during sweep 1 got sweep %s, want 2", got)
		}
	}
	if n := sweeps.Load(); n != 2 {
		t.Fatalf("sweeps = %d, want 2", n)
	}

	// The queue empties once idle; the next caller starts a sweep at once.
	res, err := c.do(ctx, "livepeer-gateway", sweep)
	if err != nil || res["sweep"] != "3" {
		t.Fatalf("idle call = %v, %v; want sweep 3", res, err)
	}
}

func TestSweepCoalescerCallerContextEndsItsWaitOnly(t *testing.T) {
	var c sweepCoalescer
	release := make(chan struct{})
	var sweepCtxErr atomic.Value
	sweep := func(sweepCtx context.Context) (map[string]string, error) {
		<-release
		sweepCtxErr.Store(sweepCtx.Err() == nil)
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.do(ctx, "livepeer-gateway", sweep)
		done <- err
	}()
	waitFor(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.byType["livepeer-gateway"] != nil
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller err = %v, want context.Canceled", err)
	}
	close(release)
	waitFor(t, func() bool { return sweepCtxErr.Load() != nil })
	if alive, _ := sweepCtxErr.Load().(bool); !alive {
		t.Fatal("the sweep was cancelled with the caller that started it; the callers sharing it would get a failed sweep")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
