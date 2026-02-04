package cache

import (
	"context"
	"errors"
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
