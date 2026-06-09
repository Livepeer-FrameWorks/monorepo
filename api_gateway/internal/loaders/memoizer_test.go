package loaders

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// The memoizer's contract is "invoke the loader exactly once per key, even under
// concurrent access" — that single-flight guarantee is what stops a burst of
// GraphQL field resolutions from fanning out into N identical backend calls.

func TestMemoizer_LoadsOncePerKeyUnderConcurrency(t *testing.T) {
	m := NewMemoizer()
	var calls int64

	const goroutines = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]any, goroutines)

	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			v, err := m.GetOrLoad("same-key", func() (any, error) {
				atomic.AddInt64(&calls, 1)
				return "value", nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			results[idx] = v
		}(i)
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("loader invoked %d times, want exactly 1", got)
	}
	for i, v := range results {
		if v != "value" {
			t.Fatalf("goroutine %d got %v, want shared value", i, v)
		}
	}
}

func TestMemoizer_DistinctKeysLoadIndependently(t *testing.T) {
	m := NewMemoizer()
	var calls int64

	for _, key := range []string{"a", "b", "c"} {
		if _, err := m.GetOrLoad(key, func() (any, error) {
			atomic.AddInt64(&calls, 1)
			return key, nil
		}); err != nil {
			t.Fatalf("GetOrLoad(%s): %v", key, err)
		}
	}
	// Re-request an existing key — should hit the memo, not the loader.
	if _, err := m.GetOrLoad("a", func() (any, error) {
		atomic.AddInt64(&calls, 1)
		return "a-again", nil
	}); err != nil {
		t.Fatalf("GetOrLoad(a) repeat: %v", err)
	}

	if got := atomic.LoadInt64(&calls); got != 3 {
		t.Fatalf("loader invoked %d times, want 3 (one per distinct key)", got)
	}
}

func TestMemoizer_CachesErrorResult(t *testing.T) {
	m := NewMemoizer()
	sentinel := errors.New("boom")
	var calls int64

	for range 3 {
		_, err := m.GetOrLoad("err-key", func() (any, error) {
			atomic.AddInt64(&calls, 1)
			return nil, sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("want sentinel error, got %v", err)
		}
	}
	// The error is memoized like any other result: the loader runs once.
	if got := atomic.LoadInt64(&calls); got != 1 {
		t.Fatalf("loader invoked %d times, want 1 (error memoized)", got)
	}
}

func TestMemoizer_NilReceiverRunsLoaderDirectly(t *testing.T) {
	var m *Memoizer
	var calls int
	v, err := m.GetOrLoad("k", func() (any, error) {
		calls++
		return 42, nil
	})
	if err != nil || v != 42 {
		t.Fatalf("got (%v, %v), want (42, nil)", v, err)
	}
	if calls != 1 {
		t.Fatalf("loader invoked %d times, want 1", calls)
	}
}
