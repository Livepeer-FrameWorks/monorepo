package control

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newPopulatedRegistry(t *testing.T) *StreamRegistry {
	t.Helper()
	r := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	return r
}

func TestAdmitAndReserve_AcceptNewWhenNoOwner(t *testing.T) {
	r := newPopulatedRegistry(t)
	got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil)
	if got.Decision != AdmissionAcceptNew {
		t.Errorf("decision = %v, want AdmissionAcceptNew", got.Decision)
	}
	if got.OldOwnerNodeID != "" {
		t.Errorf("OldOwnerNodeID = %q, want empty for new", got.OldOwnerNodeID)
	}
	// Atomic reservation: source-active flip happens in the same critical
	// section as the decision. After AcceptNew, SourceActive must be true
	// and OwnerNodeID must be the candidate.
	loc, ok := r.LocalReplication(context.Background(), "60546679b497415db2338cd5cae54992")
	_ = loc
	_ = ok
	r.mu.RLock()
	entry := r.byInt["60546679b497415db2338cd5cae54992"]
	r.mu.RUnlock()
	if entry == nil {
		t.Fatal("entry missing after accept")
	}
	l := entry.entry.Locations["cluster-A"]
	if !l.SourceActive || l.OwnerNodeID != "node-1" {
		t.Errorf("after AcceptNew: SourceActive=%v OwnerNodeID=%q, want true / node-1", l.SourceActive, l.OwnerNodeID)
	}
}

func TestAdmitAndReserve_AcceptNewWhenStreamUnknown(t *testing.T) {
	r := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	got := r.AdmitAndReserve("never-seen", "node-1", nil)
	if got.Decision != AdmissionAcceptNew {
		t.Errorf("decision = %v, want AdmissionAcceptNew", got.Decision)
	}
	// Atomic reservation creates a minimal entry so subsequent
	// concurrent PUSH_REWRITEs can't both AcceptNew.
	r.mu.RLock()
	entry, present := r.byInt["never-seen"]
	r.mu.RUnlock()
	if !present {
		t.Fatal("entry not created for unknown stream after AcceptNew")
	}
	if l := entry.entry.Locations["cluster-A"]; !l.SourceActive || l.OwnerNodeID != "node-1" {
		t.Errorf("minimal entry: SourceActive=%v OwnerNodeID=%q, want true / node-1", l.SourceActive, l.OwnerNodeID)
	}
}

func TestAdmitAndReserve_RejectWhileSourceActive(t *testing.T) {
	r := newPopulatedRegistry(t)
	// First admit owns the stream.
	if got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil); got.Decision != AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", got.Decision)
	}
	// Same-node duplicate while active rejects — only PUSH_INPUT_CLOSE
	// flips source_active to false. A second concurrent publisher on
	// the owner node is a duplicate, not resume.
	got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil)
	if got.Decision != AdmissionRejectDuplicate {
		t.Errorf("same-node while active: decision = %v, want AdmissionRejectDuplicate", got.Decision)
	}
	// Different node — reject duplicate.
	got = r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-2", nil)
	if got.Decision != AdmissionRejectDuplicate {
		t.Errorf("different-node while active: decision = %v, want AdmissionRejectDuplicate", got.Decision)
	}
	// Reject path must NOT mutate state. Owner stays node-1.
	r.mu.RLock()
	l := r.byInt["60546679b497415db2338cd5cae54992"].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if l.OwnerNodeID != "node-1" {
		t.Errorf("reject mutated owner: got %q, want node-1 untouched", l.OwnerNodeID)
	}
}

func TestAdmitAndReserve_AcceptResumeSameNodeAfterClose(t *testing.T) {
	r := newPopulatedRegistry(t)
	if got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil); got.Decision != AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", got.Decision)
	}
	r.MarkSourceInactive("60546679b497415db2338cd5cae54992", "node-1")

	got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil)
	if got.Decision != AdmissionAcceptResume {
		t.Errorf("decision = %v, want AdmissionAcceptResume", got.Decision)
	}
	// Resume re-flips SourceActive to true on the same owner.
	r.mu.RLock()
	l := r.byInt["60546679b497415db2338cd5cae54992"].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if !l.SourceActive || l.OwnerNodeID != "node-1" {
		t.Errorf("after resume: SourceActive=%v OwnerNodeID=%q, want true / node-1", l.SourceActive, l.OwnerNodeID)
	}
}

func TestAdmitAndReserve_AcceptTakeoverDifferentNodeAfterClose(t *testing.T) {
	r := newPopulatedRegistry(t)
	if got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil); got.Decision != AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", got.Decision)
	}
	r.MarkSourceInactive("60546679b497415db2338cd5cae54992", "node-1")

	got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-2", nil)
	if got.Decision != AdmissionAcceptTakeover {
		t.Fatalf("decision = %v, want AdmissionAcceptTakeover", got.Decision)
	}
	if got.OldOwnerNodeID != "node-1" {
		t.Errorf("OldOwnerNodeID = %q, want node-1", got.OldOwnerNodeID)
	}
	// Takeover atomically flips OwnerNodeID to the new node + SourceActive=true.
	r.mu.RLock()
	l := r.byInt["60546679b497415db2338cd5cae54992"].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if !l.SourceActive || l.OwnerNodeID != "node-2" {
		t.Errorf("after takeover: SourceActive=%v OwnerNodeID=%q, want true / node-2", l.SourceActive, l.OwnerNodeID)
	}
}

func TestAdmitAndReserve_OwnerUnhealthyShortCircuitsActive(t *testing.T) {
	r := newPopulatedRegistry(t)
	if got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-1", nil); got.Decision != AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", got.Decision)
	}

	// Without an ownerHealthy callback we'd reject. With node-1 reported
	// unhealthy, we treat source as inactive and allow takeover on
	// node-2 — safety net for a PUSH_INPUT_CLOSE that never arrived
	// because the owner node crashed.
	ownerHealthy := func(nodeID string) bool { return nodeID != "node-1" }
	got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-2", ownerHealthy)
	if got.Decision != AdmissionAcceptTakeover {
		t.Fatalf("decision = %v, want AdmissionAcceptTakeover", got.Decision)
	}
	if got.OldOwnerNodeID != "node-1" {
		t.Errorf("OldOwnerNodeID = %q, want node-1", got.OldOwnerNodeID)
	}
}

func TestMarkSourceInactiveIgnoresWrongOwner(t *testing.T) {
	r := newPopulatedRegistry(t)
	r.MarkSourceActive("60546679b497415db2338cd5cae54992", "node-1")
	// Stale PUSH_INPUT_CLOSE from node-2 (e.g. replay or wrong-node leak)
	// must not clear node-1's live ownership.
	r.MarkSourceInactive("60546679b497415db2338cd5cae54992", "node-2")
	got := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", "node-3", nil)
	if got.Decision != AdmissionRejectDuplicate {
		t.Errorf("decision = %v, want AdmissionRejectDuplicate (stale close must be ignored)", got.Decision)
	}
}

// TestAdmitAndReserve_RaceExactlyOneAccepts is the core reason this
// function exists. N goroutines concurrently call AdmitAndReserve for the
// same stream on different candidate nodes. Exactly one must come back
// with an accept decision; the rest must reject. Any non-atomic split
// of the decision read and the SourceActive flip would let multiple
// goroutines all read SourceActive=false and all AcceptNew, producing
// split-brain ingest.
func TestAdmitAndReserve_RaceExactlyOneAccepts(t *testing.T) {
	r := newPopulatedRegistry(t)

	const candidates = 32
	var accepts atomic.Int32
	var rejects atomic.Int32
	var wg sync.WaitGroup
	wg.Add(candidates)
	start := make(chan struct{})

	for i := 0; i < candidates; i++ {
		nodeID := nodeName(i)
		go func() {
			defer wg.Done()
			<-start
			res := r.AdmitAndReserve("60546679b497415db2338cd5cae54992", nodeID, nil)
			switch res.Decision {
			case AdmissionAcceptNew, AdmissionAcceptResume, AdmissionAcceptTakeover:
				accepts.Add(1)
			case AdmissionRejectDuplicate:
				rejects.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if accepts.Load() != 1 {
		t.Errorf("accepts = %d, want exactly 1", accepts.Load())
	}
	if rejects.Load() != candidates-1 {
		t.Errorf("rejects = %d, want %d", rejects.Load(), candidates-1)
	}
}

func nodeName(i int) string {
	return "node-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
}

func TestRuntimeNameForStream(t *testing.T) {
	r := newPopulatedRegistry(t)
	got := RuntimeNameForStream(r, "60546679b497415db2338cd5cae54992")
	if got != "60546679b497415db2338cd5cae54992" {
		t.Errorf("native: got %q, want bare internal_name", got)
	}
	if got := RuntimeNameForStream(r, "unknown"); got != "unknown" {
		t.Errorf("unknown fallback: got %q, want literal internal name", got)
	}
	if got := RuntimeNameForStream(nil, "anything"); got != "anything" {
		t.Errorf("nil registry: got %q, want literal", got)
	}
}

func TestLocalReplicationAcceptsSourceRuntimeName(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	r.MarkReplicating("stream-1", "cluster-B", "dtsc://origin/live+stream-1", "edge-a", "https://edge-a/view", "origin-node")

	loc, ok := r.LocalReplication(context.Background(), "live+stream-1")
	if !ok {
		t.Fatal("expected live+ runtime name to resolve to stored replication")
	}
	if loc.PullDTSCURL != "dtsc://origin/live+stream-1" {
		t.Fatalf("PullDTSCURL = %q", loc.PullDTSCURL)
	}
	if _, ok := r.LocalReplication(context.Background(), "pull+stream-1"); !ok {
		t.Fatal("expected pull+ runtime name to resolve to stored replication")
	}
}
