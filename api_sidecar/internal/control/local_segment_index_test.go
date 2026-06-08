package control

import (
	"testing"
	"time"
)

// newTestSegmentIndex builds an empty, logger-less index. The methods exercised
// here never touch the logger (only RestoreFromDisk does, which is out of scope).
func newTestSegmentIndex() *LocalSegmentIndex {
	return &LocalSegmentIndex{entries: map[localSegmentKey]*LocalSegmentRef{}}
}

func (idx *LocalSegmentIndex) testRef(dvrHash, seg string) (*LocalSegmentRef, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	ref, ok := idx.entries[localSegmentKey{dvrHash, seg}]
	return ref, ok
}

// EvictionEligible is the load-bearing safety predicate: a segment must never be
// reported evictable while it could still be served. Each blocking condition is
// asserted independently so a regression that drops one branch is caught.
func TestEvictionEligible_BlockingConditions(t *testing.T) {
	const ttl = time.Minute
	old := time.Now().Add(-time.Hour) // safely outside the cache TTL

	cases := []struct {
		name string
		ref  *LocalSegmentRef
		want bool
	}{
		{
			name: "all clear is eligible",
			ref:  &LocalSegmentRef{Uploaded: true, LastAccessed: old},
			want: true,
		},
		{
			name: "not uploaded blocks",
			ref:  &LocalSegmentRef{Uploaded: false, LastAccessed: old},
			want: false,
		},
		{
			name: "in rolling window blocks",
			ref:  &LocalSegmentRef{Uploaded: true, InRollingWindow: true, LastAccessed: old},
			want: false,
		},
		{
			name: "active view blocks",
			ref:  &LocalSegmentRef{Uploaded: true, ActiveViews: 1, LastAccessed: old},
			want: false,
		},
		{
			name: "pinned into the future blocks",
			ref:  &LocalSegmentRef{Uploaded: true, LastAccessed: old, PinnedUntil: time.Now().Add(time.Hour)},
			want: false,
		},
		{
			name: "expired pin does not block",
			ref:  &LocalSegmentRef{Uploaded: true, LastAccessed: old, PinnedUntil: time.Now().Add(-time.Hour)},
			want: true,
		},
		{
			name: "recent access within TTL blocks",
			ref:  &LocalSegmentRef{Uploaded: true, LastAccessed: time.Now()},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := newTestSegmentIndex()
			idx.entries[localSegmentKey{"hash", "seg"}] = tc.ref
			if got := idx.EvictionEligible("hash", "seg", ttl); got != tc.want {
				t.Fatalf("EvictionEligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvictionEligible_UnknownAndNil(t *testing.T) {
	idx := newTestSegmentIndex()
	if idx.EvictionEligible("missing", "seg", time.Minute) {
		t.Fatal("unknown key must not be evictable")
	}
	var nilIdx *LocalSegmentIndex
	if nilIdx.EvictionEligible("x", "y", time.Minute) {
		t.Fatal("nil index must not be evictable")
	}
}

// cacheTTL <= 0 disables the recency guard: a just-accessed segment becomes
// eligible the moment views/pins clear.
func TestEvictionEligible_ZeroTTLDisablesRecencyGuard(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.entries[localSegmentKey{"h", "s"}] = &LocalSegmentRef{Uploaded: true, LastAccessed: time.Now()}
	if !idx.EvictionEligible("h", "s", 0) {
		t.Fatal("with cacheTTL=0 a recently accessed segment should be eligible")
	}
}

// AcquireView/ReleaseView guard against eviction while playbacks hold a segment,
// and ReleaseView must not underflow below zero.
func TestViewRefcountNoUnderflowAndBlocksEviction(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.TrackCachedSegment("h", "s", "/p", 10, false)

	idx.AcquireView("h", "s")
	idx.AcquireView("h", "s")
	if ref, _ := idx.testRef("h", "s"); ref.ActiveViews != 2 {
		t.Fatalf("ActiveViews = %d, want 2", ref.ActiveViews)
	}
	if idx.EvictionEligible("h", "s", 0) {
		t.Fatal("segment with active views must not be evictable")
	}

	// Three releases on a count of two must clamp at zero, not go negative.
	idx.ReleaseView("h", "s")
	idx.ReleaseView("h", "s")
	idx.ReleaseView("h", "s")
	if ref, _ := idx.testRef("h", "s"); ref.ActiveViews != 0 {
		t.Fatalf("ActiveViews = %d, want 0 (no underflow)", ref.ActiveViews)
	}
	if !idx.EvictionEligible("h", "s", 0) {
		t.Fatal("segment with no views should be evictable")
	}
}

// TrackCachedSegment is an idempotent upsert: re-tracking updates the file
// details and forces Uploaded/LedgerStatus without creating a duplicate entry.
func TestTrackCachedSegmentIdempotentUpsert(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.TrackCachedSegment("h", "s", "/old", 10, false)
	idx.TrackCachedSegment("h", "s", "/new", 99, true)

	if len(idx.Snapshot()) != 1 {
		t.Fatalf("expected a single entry, got %d", len(idx.Snapshot()))
	}
	ref, ok := idx.testRef("h", "s")
	if !ok {
		t.Fatal("entry missing after upsert")
	}
	if ref.LocalPath != "/new" || ref.SizeBytes != 99 || !ref.Uploaded || !ref.ActiveRecording {
		t.Fatalf("upsert did not refresh fields: %+v", ref)
	}
	if ref.LedgerStatus != "uploaded" {
		t.Fatalf("LedgerStatus = %q, want uploaded", ref.LedgerStatus)
	}
}

// PinCachedSegment only ever extends the lease: an earlier deadline never shrinks
// a longer pin, and a zero deadline is a no-op.
func TestPinCachedSegmentMonotonic(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.TrackCachedSegment("h", "s", "/p", 10, false)

	later := time.Now().Add(2 * time.Hour)
	earlier := time.Now().Add(1 * time.Hour)

	idx.PinCachedSegment("h", "s", later)
	idx.PinCachedSegment("h", "s", earlier) // must not shorten
	if ref, _ := idx.testRef("h", "s"); !ref.PinnedUntil.Equal(later) {
		t.Fatalf("PinnedUntil = %v, want %v (earlier pin must not shorten)", ref.PinnedUntil, later)
	}

	idx.PinCachedSegment("h", "s", time.Time{}) // zero is a no-op
	if ref, _ := idx.testRef("h", "s"); !ref.PinnedUntil.Equal(later) {
		t.Fatalf("zero pin changed PinnedUntil to %v", ref.PinnedUntil)
	}
}

// Snapshot returns value copies; mutating the result must not touch the index.
func TestSnapshotIsDefensiveCopy(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.TrackCachedSegment("h", "s", "/p", 10, false)

	snap := idx.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	snap[0].SizeBytes = 123456

	if ref, _ := idx.testRef("h", "s"); ref.SizeBytes != 10 {
		t.Fatalf("mutating snapshot leaked into index: SizeBytes = %d", ref.SizeBytes)
	}
}

func TestMarkRollingWindowFlips(t *testing.T) {
	idx := newTestSegmentIndex()
	idx.TrackCachedSegment("h", "s", "/p", 10, false)

	idx.MarkRollingWindow("h", "s", true)
	if ref, _ := idx.testRef("h", "s"); !ref.InRollingWindow {
		t.Fatal("MarkRollingWindow(true) did not set the flag")
	}
	idx.MarkRollingWindow("h", "s", false)
	if ref, _ := idx.testRef("h", "s"); ref.InRollingWindow {
		t.Fatal("MarkRollingWindow(false) did not clear the flag")
	}
}
