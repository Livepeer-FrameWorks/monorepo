package balancer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedFanOut_DedupsConcurrentCalls(t *testing.T) {
	sf := NewSharedFanOut(time.Minute)
	var calls atomic.Int32

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := sf.Do("k1", func() []RemoteEdgeCandidate {
				calls.Add(1)
				time.Sleep(50 * time.Millisecond)
				return []RemoteEdgeCandidate{{NodeID: "n1"}}
			})
			if len(got) != 1 {
				t.Errorf("got %d candidates, want shared result of 1", len(got))
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("fn calls = %d, want 1", got)
	}
}

func TestSharedFanOut_MemoizesIncludingEmptyAndExpires(t *testing.T) {
	sf := NewSharedFanOut(30 * time.Millisecond)
	var calls atomic.Int32
	empty := func() []RemoteEdgeCandidate {
		calls.Add(1)
		return nil
	}

	if got := sf.Do("k1", empty); got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
	if !sf.Memoized("k1") {
		t.Fatal("empty result not memoized")
	}
	_ = sf.Do("k1", empty)
	if got := calls.Load(); got != 1 {
		t.Fatalf("fn calls within TTL = %d, want 1", got)
	}

	// Distinct keys do not share memo or flight.
	_ = sf.Do("k2", empty)
	if got := calls.Load(); got != 2 {
		t.Fatalf("fn calls after second key = %d, want 2", got)
	}

	// After expiry the fn runs again (and the stale entry is pruned).
	time.Sleep(40 * time.Millisecond)
	_ = sf.Do("k1", empty)
	if got := calls.Load(); got != 3 {
		t.Fatalf("fn calls after expiry = %d, want 3", got)
	}
}
