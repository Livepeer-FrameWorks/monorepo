package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStoreNegativeTTLZeroDoesNotStore(t *testing.T) {
	var errored int
	c := New(Options{TTL: time.Minute, NegativeTTL: 0, MaxEntries: 10}, MetricsHooks{
		OnError: func(map[string]string) { errored++ },
	})
	c.store("k", nil, false, errors.New("boom"))
	if _, ok := c.Peek("k"); ok {
		t.Fatalf("expected no entry stored for NegativeTTL=0")
	}
	if len(c.Snapshot()) != 0 {
		t.Fatalf("expected empty cache, got %d entries", len(c.Snapshot()))
	}
	if errored != 1 {
		t.Fatalf("expected OnError to fire once, got %d", errored)
	}
}

func TestStoreNegativeTTLPositiveStoresNegative(t *testing.T) {
	c := New(Options{TTL: time.Minute, NegativeTTL: time.Minute, MaxEntries: 10}, MetricsHooks{})
	errBoom := errors.New("boom")
	c.store("k", nil, false, errBoom)
	snap := c.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 negative entry, got %d", len(snap))
	}
	if !snap[0].Negative {
		t.Fatalf("expected entry to be negative")
	}
	// negative entries are not peekable
	if _, ok := c.Peek("k"); ok {
		t.Fatalf("expected negative entry not peekable")
	}
	_, ok, err := c.Get(context.Background(), "k", func(context.Context, string) (interface{}, bool, error) {
		t.Fatal("loader should not run for cached negative")
		return nil, false, nil
	})
	if ok || !errors.Is(err, errBoom) {
		t.Fatalf("expected cached negative error, got ok=%v err=%v", ok, err)
	}
}

func TestRemoveFromOrderRemovesCorrectKey(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 10}, MetricsHooks{})
	c.Set("a", 1, time.Minute)
	c.Set("b", 2, time.Minute)
	c.Set("c", 3, time.Minute)

	c.Delete("b")

	if len(c.order) != 2 {
		t.Fatalf("expected 2 order entries, got %d (%v)", len(c.order), c.order)
	}
	if c.order[0] != "a" || c.order[1] != "c" {
		t.Fatalf("expected order [a c], got %v", c.order)
	}
	if _, ok := c.Peek("b"); ok {
		t.Fatalf("expected b removed")
	}
	if _, ok := c.Peek("a"); !ok {
		t.Fatalf("expected a retained")
	}
	if _, ok := c.Peek("c"); !ok {
		t.Fatalf("expected c retained")
	}
}

func TestRemoveFromOrderMissingKeyIsNoop(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 10}, MetricsHooks{})
	c.Set("a", 1, time.Minute)
	c.Set("b", 2, time.Minute)
	c.removeFromOrder("absent")
	if len(c.order) != 2 || c.order[0] != "a" || c.order[1] != "b" {
		t.Fatalf("expected order unchanged [a b], got %v", c.order)
	}
}

func TestEvictIfNeededDisabledWhenMaxEntriesNonPositive(t *testing.T) {
	for _, max := range []int{0, -1} {
		c := New(Options{TTL: time.Minute, MaxEntries: max}, MetricsHooks{})
		for _, k := range []string{"a", "b", "c", "d"} {
			c.Set(k, k, time.Minute)
		}
		if len(c.Snapshot()) != 4 {
			t.Fatalf("MaxEntries=%d: expected no eviction (4 entries), got %d", max, len(c.Snapshot()))
		}
	}
}

func TestEvictIfNeededBoundaryAtCapacity(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 2}, MetricsHooks{})
	c.Set("a", 1, time.Minute)
	c.Set("b", 2, time.Minute)
	// exactly at capacity: nothing evicted
	if _, ok := c.Peek("a"); !ok {
		t.Fatalf("expected a retained at capacity")
	}
	if _, ok := c.Peek("b"); !ok {
		t.Fatalf("expected b retained at capacity")
	}
	if len(c.Snapshot()) != 2 {
		t.Fatalf("expected 2 entries at capacity, got %d", len(c.Snapshot()))
	}

	// one over capacity: oldest (a) evicted, exactly one removed
	c.Set("c", 3, time.Minute)
	if _, ok := c.Peek("a"); ok {
		t.Fatalf("expected a evicted")
	}
	if _, ok := c.Peek("b"); !ok {
		t.Fatalf("expected b retained")
	}
	if _, ok := c.Peek("c"); !ok {
		t.Fatalf("expected c retained")
	}
	if len(c.Snapshot()) != 2 {
		t.Fatalf("expected exactly 2 entries after eviction, got %d", len(c.Snapshot()))
	}
}

func TestEvictIfNeededRemovesOnlyExcess(t *testing.T) {
	c := New(Options{TTL: time.Minute, MaxEntries: 3}, MetricsHooks{})
	for _, k := range []string{"a", "b", "c"} {
		c.Set(k, k, time.Minute)
	}
	// jump two over capacity in one shot to exercise the eviction loop counter
	c.items["d"] = &entry{value: "d", expiresAt: time.Now().Add(time.Minute), staleAt: time.Now().Add(time.Minute)}
	c.order = append(c.order, "d")
	c.items["e"] = &entry{value: "e", expiresAt: time.Now().Add(time.Minute), staleAt: time.Now().Add(time.Minute)}
	c.order = append(c.order, "e")
	c.evictIfNeeded()
	if len(c.items) != 3 {
		t.Fatalf("expected 3 entries after eviction, got %d", len(c.items))
	}
	// FIFO: a and b evicted, c/d/e remain
	for _, gone := range []string{"a", "b"} {
		if _, ok := c.items[gone]; ok {
			t.Fatalf("expected %s evicted", gone)
		}
	}
	for _, kept := range []string{"c", "d", "e"} {
		if _, ok := c.items[kept]; !ok {
			t.Fatalf("expected %s retained", kept)
		}
	}
}
