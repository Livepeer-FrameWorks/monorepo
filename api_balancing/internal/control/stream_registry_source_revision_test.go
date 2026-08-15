package control

import (
	"testing"
	"time"
)

// The registry merge must protect source OWNERSHIP by SourceRevision (highest wins), independent of
// the last-writer-wins UpdatedAt used for the rest of the Location — so a stale replica's unrelated
// location write cannot clobber the real publisher's source ownership.
func TestMergeLocationRevisioned_SourceOwnershipByRevision(t *testing.T) {
	tEarly := time.Unix(1000, 0)
	tLate := time.Unix(2000, 0)

	// A higher source revision survives a later unrelated location write carrying stale ownership.
	current := Location{OwnerNodeID: "node-B", SourceGeneration: "gen-B", SourceActive: true, SourceRevision: 8, UpdatedAt: tEarly}
	staleReplication := Location{
		OwnerNodeID: "node-A", SourceGeneration: "gen-A", SourceActive: true, SourceRevision: 5, // stale source
		ReplicatingFrom: "peer-X", IsLiveNow: true, UpdatedAt: tLate, // fresh, unrelated location update
	}
	merged := mergeLocationRevisioned(current, staleReplication)
	if merged.OwnerNodeID != "node-B" || merged.SourceGeneration != "gen-B" || merged.SourceRevision != 8 {
		t.Fatalf("a stale replica clobbered the real publisher's source: owner=%q gen=%q rev=%d",
			merged.OwnerNodeID, merged.SourceGeneration, merged.SourceRevision)
	}
	if merged.ReplicatingFrom != "peer-X" || !merged.IsLiveNow {
		t.Fatalf("the unrelated replication update must still apply: %+v", merged)
	}

	// A higher source revision wins even with an older UpdatedAt.
	older := Location{OwnerNodeID: "node-A", SourceGeneration: "gen-A", SourceRevision: 5, UpdatedAt: tLate}
	newerSource := Location{OwnerNodeID: "node-C", SourceGeneration: "gen-C", SourceActive: true, SourceRevision: 9, UpdatedAt: tEarly}
	m2 := mergeLocationRevisioned(older, newerSource)
	if m2.OwnerNodeID != "node-C" || m2.SourceRevision != 9 || !m2.SourceActive {
		t.Fatalf("a higher-revision projection must win regardless of UpdatedAt: %+v", m2)
	}

	// Pull ownership has revision zero and uses UpdatedAt ordering.
	a := Location{OwnerNodeID: "node-A", SourceRevision: 0, UpdatedAt: tEarly}
	b := Location{OwnerNodeID: "node-B", SourceRevision: 0, UpdatedAt: tLate}
	if m3 := mergeLocationRevisioned(a, b); m3.OwnerNodeID != "node-B" {
		t.Fatalf("equal (0) revision must fall back to UpdatedAt (newer wins): owner=%q", m3.OwnerNodeID)
	}
}

// A lower revision or an equal revision carrying another publisher identity mutates nothing.
func TestProjectSource_RejectsNonIncreasingRevisionWithoutClobber(t *testing.T) {
	const stream = "60546679b497415db2338cd5cae54992"
	r := newPopulatedRegistry(t)
	locOf := func() Location {
		r.mu.RLock()
		defer r.mu.RUnlock()
		return r.byInt[stream].entry.Locations["cluster-A"]
	}

	r.ProjectSource(stream, "node-1", 100, "u1", "gen-1", 5)
	if l := locOf(); l.SourceRevision != 5 || l.OwnerNodeID != "node-1" || l.SourceGeneration != "gen-1" {
		t.Fatalf("after first project: %+v, want rev=5 owner=node-1 gen=gen-1", l)
	}

	// A LOWER-revision projection from a DIFFERENT publisher must be REJECTED WHOLE — ownership AND
	// revision unchanged, not "keep rev=5 but overwrite owner to node-2".
	r.ProjectSource(stream, "node-2", 200, "u2", "gen-STALE", 3)
	if l := locOf(); l.SourceRevision != 5 || l.OwnerNodeID != "node-1" || l.SourceGeneration != "gen-1" {
		t.Fatalf("a non-increasing projection clobbered ownership: %+v, want unchanged rev=5 owner=node-1 gen=gen-1", l)
	}

	if _, applied, err := r.ProjectSource(stream, "node-2", 200, "u2", "gen-OTHER", 5); err != nil || applied {
		t.Fatalf("equal revision with another identity applied=%v err=%v", applied, err)
	}
	if l := locOf(); l.SourceRevision != 5 || l.OwnerNodeID != "node-1" || l.SourceGeneration != "gen-1" {
		t.Fatalf("an equal revision changed publisher identity: %+v", l)
	}

	// A strictly-higher revision applies fully (owner + generation + revision).
	r.ProjectSource(stream, "node-3", 300, "u3", "gen-3", 9)
	if l := locOf(); l.SourceRevision != 9 || l.OwnerNodeID != "node-3" || l.SourceGeneration != "gen-3" {
		t.Fatalf("a higher revision must apply fully: %+v, want rev=9 owner=node-3 gen=gen-3", l)
	}
}
