package policybundle

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetServesWhenVersionEqualsWatermark pins the watermark acceptance
// boundary: an entry whose BundleVersion exactly equals the watermark is still
// acceptable (>=), so a fresh entry at the watermark serves from cache without
// a fetch. A strict-greater-than mutant would force an unnecessary fetch here.
func TestGetServesWhenVersionEqualsWatermark(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(5, 60*time.Second, 30*time.Minute, now))
	c.BumpWatermark("t1", "s1", 5) // watermark == cached version

	calls := atomic.Int32{}
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		calls.Add(1)
		return Entry{}, errors.New("should not fetch when version == watermark")
	}

	e, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.BundleVersion != 5 {
		t.Errorf("got version %d, want 5 (served from cache)", e.BundleVersion)
	}
	if calls.Load() != 0 {
		t.Errorf("fetch called %d times; version == watermark must serve cached", calls.Load())
	}
}

// TestGetFetchesWhenVersionBelowWatermark is the other side of the boundary:
// BundleVersion strictly below the watermark must force a synchronous fetch.
func TestGetFetchesWhenVersionBelowWatermark(t *testing.T) {
	c := New()
	now := time.Now()
	c.putEntry(cacheKey("t1", "s1"), newEntry(4, 60*time.Second, 30*time.Minute, now))
	c.BumpWatermark("t1", "s1", 5)

	calls := atomic.Int32{}
	fetch := func(_ context.Context, _, _ string) (Entry, error) {
		calls.Add(1)
		return newEntry(5, 60*time.Second, 30*time.Minute, time.Now()), nil
	}

	e, err := c.Get(context.Background(), "t1", "s1", fetch, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if e.BundleVersion != 5 {
		t.Errorf("got version %d, want 5 (fetched, under watermark)", e.BundleVersion)
	}
	if calls.Load() != 1 {
		t.Errorf("fetch called %d times, want 1 (under-watermark forces fetch)", calls.Load())
	}
}
