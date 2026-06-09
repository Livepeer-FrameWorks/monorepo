package cache

import (
	"testing"
	"time"
)

// TestGetFresh_ZeroMaxAgeFallsBackToTTL pins the maxAge<=0 guard: a caller
// passing maxAge=0 must fall back to the cache TTL, not treat the entry as
// instantly stale.
func TestGetFresh_ZeroMaxAgeFallsBackToTTL(t *testing.T) {
	c := NewLRU(1024, 1*time.Hour)
	c.Put("k1", []byte("data"), "text/plain")

	data, _, ok := c.GetFresh("k1", 0)
	if !ok {
		t.Fatal("maxAge=0 must fall back to TTL → expected hit")
	}
	if string(data) != "data" {
		t.Errorf("data = %q, want %q", data, "data")
	}

	// Negative maxAge takes the same fallback branch.
	if _, _, ok := c.GetFresh("k1", -1*time.Second); !ok {
		t.Fatal("negative maxAge must fall back to TTL → expected hit")
	}
}

// TestPut_OversizedItemIntoEmptyCacheDoesNotEvictNothing pins the eviction
// loop guard order.Len() > 0: an item larger than maxBytes put into an empty
// cache must not attempt to evict from an empty list (which would panic on a
// nil Back()).
func TestPut_OversizedItemIntoEmptyCacheDoesNotEvictNothing(t *testing.T) {
	c := NewLRU(2, 5*time.Minute)
	c.Put("big", []byte("oversized"), "t") // 9 bytes > 2 max, empty list → no eviction

	if c.Len() != 1 {
		t.Fatalf("oversized item should be stored, len=%d want 1", c.Len())
	}
	data, _, ok := c.Get("big")
	if !ok || string(data) != "oversized" {
		t.Fatalf("oversized item should be retrievable, got ok=%v data=%q", ok, data)
	}
}
