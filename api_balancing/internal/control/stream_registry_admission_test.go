package control

import (
	"context"
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

func projectSourceForTest(t *testing.T, r *StreamRegistry, stream, node string, pid int64, triggerUUID, generation string, revision int64) string {
	t.Helper()
	prior, applied, err := r.ProjectSource(stream, node, pid, triggerUUID, generation, revision)
	if err != nil {
		t.Fatalf("project source: %v", err)
	}
	if !applied {
		t.Fatalf("project source revision %d was not applied", revision)
	}
	return prior
}

func markSourceInactiveForTest(t *testing.T, r *StreamRegistry, stream, node, generation string, revision int64) bool {
	t.Helper()
	flipped, err := r.PublishSourceInactive(stream, node, generation, revision)
	if err != nil {
		t.Fatalf("mark source inactive: %v", err)
	}
	return flipped
}

// ProjectSource is the DB winner's projection: it sets SourceActive + owner + PID + trigger UUID +
// generation on the local Location, publishes, and a later projection (a reconnect) supersedes the
// generation — which is what the offline fence keys on.
func TestProjectSource_ProjectsWinnerAndStampsGeneration(t *testing.T) {
	const stream = "60546679b497415db2338cd5cae54992"
	r := newPopulatedRegistry(t)

	projectSourceForTest(t, r, stream, "node-1", 100, "uuid-1", "gen-1", 1)
	if gen, active, ok := r.SourceGenerationSnapshot(stream, "node-1"); !ok || !active || gen != "gen-1" {
		t.Fatalf("after projection: ok=%v active=%v gen=%q, want active on gen-1", ok, active, gen)
	}
	r.mu.RLock()
	l := r.byInt[stream].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if l.OwnerNodeID != "node-1" || l.SourceConnectorPID != 100 || l.SourceTriggerUUID != "uuid-1" || l.SourceGeneration != "gen-1" {
		t.Fatalf("projected location = %+v, want owner=node-1 pid=100 uuid=uuid-1 gen=gen-1", l)
	}

	// A reconnect projection supersedes the generation (a globally-unique DB session id).
	projectSourceForTest(t, r, stream, "node-1", 100, "uuid-1", "gen-2", 2)
	if gen, _, ok := r.SourceGenerationSnapshot(stream, "node-1"); !ok || gen != "gen-2" {
		t.Fatalf("after reconnect projection: gen=%q ok=%v, want gen-2", gen, ok)
	}
}

func TestPublishSourceInactive_RollsBackLocalStateWhenRedisHasNewerRevision(t *testing.T) {
	const stream = "60546679b497415db2338cd5cae54992"
	r := newPopulatedRegistry(t)
	projectSourceForTest(t, r, stream, "node-old", 100, "trigger-old", "gen-old", 1)

	store, _, _ := newTestRedis(t)
	newer := StreamEntry{InternalName: stream, Locations: map[string]Location{
		"cluster-test": {
			ClusterID:        "cluster-test",
			SourceActive:     true,
			OwnerNodeID:      "node-new",
			SourceGeneration: "gen-new",
			SourceRevision:   3,
		},
	}}
	change := RegistryChange{InstanceID: "peer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: stream, SourceRevision: 3}
	if applied, err := store.SetSourceRevisioned(context.Background(), newer, change, 3); err != nil || !applied {
		t.Fatalf("seed newer Redis source: applied=%v err=%v", applied, err)
	}
	r.mu.Lock()
	r.redisStore = store
	r.instanceID = "local"
	r.mu.Unlock()

	applied, err := r.PublishSourceInactive(stream, "node-old", "gen-old", 2)
	if err != nil {
		t.Fatalf("publish inactive: %v", err)
	}
	if applied {
		t.Fatal("stale inactive transition unexpectedly won Redis revision CAS")
	}
	if generation, active, ok := r.SourceGenerationSnapshot(stream, "node-old"); !ok || !active || generation != "gen-old" {
		t.Fatalf("local state after lost CAS = generation %q active=%v ok=%v, want original active gen-old", generation, active, ok)
	}
}

// ProjectSource reports the node it REPLACED so the processor can drain it. This is the drain
// authority: it fires on the ACTUAL projection change, so a stale projection whose owner was still
// marked active+healthy (the plan would predict Resume) is STILL surfaced for drain once the DB admits
// a different node. A new source or a same-node reprojection reports no prior owner.
func TestProjectSource_ReportsPriorOwnerOnNodeChange(t *testing.T) {
	const stream = "60546679b497415db2338cd5cae54992"
	r := newPopulatedRegistry(t)

	// First projection: no prior owner.
	if prior := projectSourceForTest(t, r, stream, "node-A", 100, "uuid-a", "gen-a", 1); prior != "" {
		t.Fatalf("first projection prior owner = %q, want empty", prior)
	}
	// Same node reprojection (a resume): still no prior owner to drain.
	if prior := projectSourceForTest(t, r, stream, "node-A", 100, "uuid-a", "gen-a2", 2); prior != "" {
		t.Fatalf("same-node reprojection prior owner = %q, want empty", prior)
	}
	// A different node may project while the registry still shows the ended session's owner.
	// The replaced owner must be returned for drain from the actual DB-confirmed projection.
	// node-A MUST be reported for drain regardless of its stale active state.
	if prior := projectSourceForTest(t, r, stream, "node-B", 200, "uuid-b", "gen-b", 3); prior != "node-A" {
		t.Fatalf("cross-node projection prior owner = %q, want node-A (must be drained)", prior)
	}
}

// TestMarkSourceOwnerIfUnset locks the pull-ownership stamp contract:
// a missing entry is created minimally (callers stamp only after a
// positive resolve, so a cold local cache must not degrade the stream to
// backstop-only offline), the first dialer wins atomically, a later
// caller never flips ownership, and an inactive projection retains the owner
// so aggregate offline events remain correctly typed.
func TestMarkSourceOwnerIfUnset(t *testing.T) {
	const internal = "60546679b497415db2338cd5cae54992"

	empty := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	if owner, stamped := empty.MarkSourceOwnerIfUnset("never-seen", "node-1"); !stamped || owner != "node-1" {
		t.Fatalf("cold-cache stamp = (%q, %v), want minimal entry created and (node-1, true)", owner, stamped)
	}
	if got, known := empty.SourceOwner("never-seen"); !known || got != "node-1" {
		t.Fatalf("SourceOwner after cold-cache stamp = (%q, %v), want (node-1, true)", got, known)
	}

	r := newPopulatedRegistry(t)
	owner, stamped := r.MarkSourceOwnerIfUnset(internal, "node-1")
	if !stamped || owner != "node-1" {
		t.Fatalf("first stamp = (%q, %v), want (node-1, true)", owner, stamped)
	}
	if got, known := r.SourceOwner(internal); !known || got != "node-1" {
		t.Fatalf("SourceOwner after stamp = (%q, %v), want (node-1, true)", got, known)
	}

	// Second caller must not clobber.
	owner, stamped = r.MarkSourceOwnerIfUnset(internal, "node-2")
	if stamped || owner != "node-1" {
		t.Fatalf("second stamp = (%q, %v), want retained (node-1, false)", owner, stamped)
	}

	// Ownership survives the source-inactive flip (reconnect/resume path).
	markSourceInactiveForTest(t, r, internal, "node-1", "", 1)
	if got, known := r.SourceOwner(internal); !known || got != "node-1" {
		t.Fatalf("SourceOwner after inactive = (%q, %v), want retained (node-1, true)", got, known)
	}
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

func TestClearReplicatingForNodeOnlyClearsPinnedNode(t *testing.T) {
	r := NewStreamRegistry(nil, "cluster-A", time.Minute)
	r.MarkReplicating("stream-1", "cluster-B", "dtsc://origin/live+stream-1", "edge-a", "https://edge-a/view", "origin-node")

	if cleared := r.ClearReplicatingForNode("stream-1", "edge-b"); cleared {
		t.Fatal("expected wrong node not to clear replication")
	}
	if _, ok := r.LocalReplication(context.Background(), "stream-1"); !ok {
		t.Fatal("expected replication to remain after wrong-node clear")
	}

	if cleared := r.ClearReplicatingForNode("stream-1", "edge-a"); !cleared {
		t.Fatal("expected pinned node to clear replication")
	}
	if _, ok := r.LocalReplication(context.Background(), "stream-1"); ok {
		t.Fatal("expected replication to be cleared")
	}
	r.mu.RLock()
	loc := r.byInt["stream-1"].entry.Locations["cluster-A"]
	r.mu.RUnlock()
	if loc.IsLiveNow {
		t.Fatal("expected local liveness to clear with replication")
	}
}
