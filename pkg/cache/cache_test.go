package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCacheSetPeekDeleteSnapshot(t *testing.T) {
	c := New(Options{TTL: 50 * time.Millisecond, StaleWhileRevalidate: 20 * time.Millisecond, MaxEntries: 10}, MetricsHooks{})

	c.Set("alpha", "value", 50*time.Millisecond)
	if val, ok := c.Peek("alpha"); !ok || val.(string) != "value" {
		t.Fatalf("expected peeked value")
	}

	snapshot := c.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Key != "alpha" {
		t.Fatalf("expected snapshot to include alpha")
	}

	c.Delete("alpha")
	if _, ok := c.Peek("alpha"); ok {
		t.Fatalf("expected key to be deleted")
	}
}

func TestCacheGetHitMissStaleRefresh(t *testing.T) {
	c := New(Options{TTL: 20 * time.Millisecond, StaleWhileRevalidate: 50 * time.Millisecond, MaxEntries: 10}, MetricsHooks{})

	var mu sync.Mutex
	callCount := 0
	refreshCalled := make(chan struct{}, 1)
	loader := func(_ context.Context, _ string) (interface{}, bool, error) {
		mu.Lock()
		callCount++
		count := callCount
		mu.Unlock()
		if count == 2 {
			refreshCalled <- struct{}{}
		}
		return count, true, nil
	}

	val, ok, err := c.Get(context.Background(), "alpha", loader)
	if err != nil || !ok || val.(int) != 1 {
		t.Fatalf("expected first load")
	}

	val, ok, err = c.Get(context.Background(), "alpha", loader)
	if err != nil || !ok || val.(int) != 1 {
		t.Fatalf("expected cache hit")
	}

	time.Sleep(25 * time.Millisecond)
	val, ok, err = c.Get(context.Background(), "alpha", loader)
	if err != nil || !ok || val.(int) != 1 {
		t.Fatalf("expected stale value")
	}

	select {
	case <-refreshCalled:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("expected refresh to run")
	}

	time.Sleep(10 * time.Millisecond)
	val, ok = c.Peek("alpha")
	if !ok || val.(int) != 2 {
		t.Fatalf("expected refreshed value")
	}
}

func TestCacheNegativeTTL(t *testing.T) {
	c := New(Options{TTL: 50 * time.Millisecond, StaleWhileRevalidate: 20 * time.Millisecond, NegativeTTL: 30 * time.Millisecond, MaxEntries: 10}, MetricsHooks{})

	var mu sync.Mutex
	callCount := 0
	errBoom := errors.New("boom")
	loader := func(_ context.Context, _ string) (interface{}, bool, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil, false, errBoom
	}

	_, ok, err := c.Get(context.Background(), "neg", loader)
	if ok || err == nil {
		t.Fatalf("expected negative load error")
	}

	_, ok, err = c.Get(context.Background(), "neg", loader)
	if ok || err == nil {
		t.Fatalf("expected cached negative error")
	}

	mu.Lock()
	firstCount := callCount
	mu.Unlock()
	if firstCount != 1 {
		t.Fatalf("expected single loader call, got %d", firstCount)
	}

	time.Sleep(35 * time.Millisecond)
	_, _, _ = c.Get(context.Background(), "neg", loader)

	mu.Lock()
	secondCount := callCount
	mu.Unlock()
	if secondCount < 2 {
		t.Fatalf("expected loader to run after negative ttl")
	}
}

func TestCacheEviction(t *testing.T) {
	c := New(Options{TTL: time.Minute, StaleWhileRevalidate: 0, MaxEntries: 2}, MetricsHooks{})

	c.Set("first", "one", time.Minute)
	c.Set("second", "two", time.Minute)
	c.Set("third", "three", time.Minute)

	if _, ok := c.Peek("first"); ok {
		t.Fatalf("expected first entry to be evicted")
	}
	if _, ok := c.Peek("second"); !ok {
		t.Fatalf("expected second entry to remain")
	}
	if _, ok := c.Peek("third"); !ok {
		t.Fatalf("expected third entry to remain")
	}
}

func TestCacheDeleteFencesInFlightLoad(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 2}, MetricsHooks{})
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		_, _, _ = c.Get(context.Background(), "tenant-1", func(_ context.Context, _ string) (interface{}, bool, error) {
			close(started)
			<-release
			return "pre-invalidation", true, nil
		})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("loader did not start")
	}
	c.Delete("tenant-1")
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loader did not finish")
	}
	if value, ok := c.Peek("tenant-1"); ok {
		t.Fatalf("in-flight load repopulated invalidated key with %v", value)
	}
}

func TestCacheSharedLoadOutlivesShortLeader(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 2}, MetricsHooks{})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	loader := func(ctx context.Context, _ string) (interface{}, bool, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		close(started)
		select {
		case <-release:
			return "healthy", true, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}

	shortCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	shortResult := make(chan error, 1)
	go func() {
		_, _, err := c.Get(shortCtx, "tenant-1", loader)
		shortResult <- err
	}()
	<-started

	longResult := make(chan error, 1)
	go func() {
		value, ok, err := c.Get(context.Background(), "tenant-1", loader)
		if err == nil && (!ok || value != "healthy") {
			err = errors.New("long waiter did not receive shared healthy result")
		}
		longResult <- err
	}()

	if err := <-shortResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short waiter error = %v, want deadline exceeded", err)
	}
	close(release)
	if err := <-longResult; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("loader calls = %d, want one shared load", calls)
	}
}

func TestCacheSWRRefreshOutlivesTriggeringCaller(t *testing.T) {
	c := New(Options{TTL: time.Millisecond, StaleWhileRevalidate: time.Second, MaxEntries: 2}, MetricsHooks{})
	c.Set("tenant-1", "stale", time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	started := make(chan struct{})
	release := make(chan struct{})
	callerCtx, cancel := context.WithCancel(context.Background())
	value, ok, err := c.Get(callerCtx, "tenant-1", func(ctx context.Context, _ string) (interface{}, bool, error) {
		close(started)
		select {
		case <-release:
			return "fresh", true, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	})
	if err != nil || !ok || value != "stale" {
		t.Fatalf("stale get = (%v, %t, %v), want stale hit", value, ok, err)
	}
	<-started
	cancel()
	close(release)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if value, found := c.Peek("tenant-1"); found && value == "fresh" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("SWR refresh was canceled with the triggering caller")
}

func TestCacheValueLifetimeAppliesToLoadsAndSetDefault(t *testing.T) {
	c := New(Options{
		TTL:                  time.Hour,
		StaleWhileRevalidate: time.Hour,
		MaxEntries:           2,
		ValueLifetime: func(_ string, value interface{}) (time.Duration, time.Duration) {
			if value == "denied" {
				return time.Millisecond, 0
			}
			return time.Hour, time.Hour
		},
	}, MetricsHooks{})

	if _, ok, err := c.Get(context.Background(), "loaded", func(context.Context, string) (interface{}, bool, error) {
		return "denied", true, nil
	}); err != nil || !ok {
		t.Fatalf("load denied value: ok=%t err=%v", ok, err)
	}
	c.SetDefault("seeded", "denied")
	time.Sleep(5 * time.Millisecond)
	if value, ok := c.PeekHardExpired("loaded"); !ok || value != "denied" {
		t.Fatalf("PeekHardExpired(loaded) = (%v, %t), want expired denied value", value, ok)
	}
	if _, ok := c.Peek("loaded"); ok {
		t.Fatal("loaded denied value outlived its value-specific TTL")
	}
	if _, ok := c.Peek("seeded"); ok {
		t.Fatal("seeded denied value outlived its value-specific TTL")
	}
}

func TestCacheInvalidationFencesDoNotRetainHistoricalKeys(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 10}, MetricsHooks{})
	for i := range 10000 {
		c.Delete(fmt.Sprintf("tenant-%d", i))
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if got := len(c.activeLoads); got != 0 {
		t.Fatalf("active load fences = %d after idle invalidation churn, want 0", got)
	}
}

func TestCacheInvalidationFenceIsReclaimedAfterCanceledLoad(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 2}, MetricsHooks{})
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = c.Get(context.Background(), "tenant-1", func(context.Context, string) (interface{}, bool, error) {
			close(started)
			<-release
			return "old", true, nil
		})
	}()
	<-started
	c.Delete("tenant-1")
	close(release)
	<-done

	c.mu.RLock()
	defer c.mu.RUnlock()
	if got := len(c.activeLoads); got != 0 {
		t.Fatalf("active load fences = %d after invalidated load completed, want 0", got)
	}
}
