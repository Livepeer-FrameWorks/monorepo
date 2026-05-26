package control

import (
	"slices"
	"testing"

	"frameworks/api_balancing/internal/state"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestPlacementPick_Deterministic(t *testing.T) {
	nodes := []string{"edge-a", "edge-b", "edge-c", "edge-d", "edge-e"}
	first := placementPick("stream-xyz", nodes, 2)
	second := placementPick("stream-xyz", nodes, 2)
	if !slices.Equal(first, second) {
		t.Fatalf("non-deterministic: %v vs %v", first, second)
	}
	if len(first) != 2 {
		t.Fatalf("want count=2, got %v", first)
	}
}

func TestPlacementPick_DifferentStreamsSpread(t *testing.T) {
	// Different stream IDs should not all map to the same starting node.
	nodes := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	seen := make(map[string]int)
	for _, id := range []string{
		"frameworks-demo", "northwind-live", "linear-channel-42",
		"announce-loop", "synthwave-radio", "test-pattern",
	} {
		pick := placementPick(id, nodes, 1)
		if len(pick) != 1 {
			t.Fatalf("%s: want count=1, got %v", id, pick)
		}
		seen[pick[0]]++
	}
	if len(seen) < 2 {
		t.Fatalf("placement collapsed to a single node: %v", seen)
	}
}

func TestPlacementPick_CountClamps(t *testing.T) {
	nodes := []string{"a", "b"}
	got := placementPick("anything", nodes, 99)
	if len(got) != 2 {
		t.Fatalf("want all 2 nodes when count > len, got %v", got)
	}
}

func TestPlacementPick_EmptyInputs(t *testing.T) {
	if got := placementPick("x", nil, 1); got != nil {
		t.Fatalf("nil nodes should produce nil, got %v", got)
	}
	if got := placementPick("x", []string{"a"}, 0); got != nil {
		t.Fatalf("count=0 should produce nil, got %v", got)
	}
}

func TestPlacementPick_RemoveOneNodeShiftsByOne(t *testing.T) {
	// Adding/removing a node should shift placement by ~1 (rotation
	// property of mod indexing), not reshuffle everything.
	all := []string{"a", "b", "c", "d", "e", "f"}
	full := placementPick("stream-1", all, 1)[0]

	// Remove the elected node from the eligible set; reconciler must pick a
	// different one. We can't assert which, but it must be one of the others.
	remaining := make([]string, 0, len(all)-1)
	for _, n := range all {
		if n != full {
			remaining = append(remaining, n)
		}
	}
	got := placementPick("stream-1", remaining, 1)
	if slices.Contains(remaining, got[0]) == false {
		t.Fatalf("re-pick on remaining nodes returned outside set: %v", got)
	}
	if got[0] == full {
		t.Fatalf("expected a different node when original is removed")
	}
}

// TestManagedStreamVerifiedAppliedMatches_DetectsSourceDrift locks the
// snapshot-matching contract: a verified-applied entry that has the same
// stream_id but a different apply key (e.g. an UPDATE whose Mist add
// failed and left the previous config visible) MUST NOT count as
// verified. Without this gate, presence-only checking would mask Mist
// add failures on update and pin routing at a stale config.
func TestManagedStreamVerifiedAppliedMatches_DetectsSourceDrift(t *testing.T) {
	managedStreamVerifiedApplied.Lock()
	managedStreamVerifiedApplied.m = make(map[string]map[string]managedStreamSnapshot)
	managedStreamVerifiedApplied.Unlock()
	t.Cleanup(func() {
		managedStreamVerifiedApplied.Lock()
		managedStreamVerifiedApplied.m = make(map[string]map[string]managedStreamSnapshot)
		managedStreamVerifiedApplied.Unlock()
	})

	UpdateVerifiedAppliedFromHeartbeat("edge-1", []*pb.AppliedManagedStream{
		{
			Name:       "frameworks-demo",
			Source:     "ts-exec:cat /dev/null",
			AlwaysOn:   true,
			IngestMode: "mist_native",
			StreamId:   "stream-uuid",
		},
	})

	matchingDesired := managedStreamSnapshot{
		sourceSpec: "ts-exec:cat /dev/null",
		alwaysOn:   true,
		ingestMode: "mist_native",
	}
	if !managedStreamVerifiedAppliedMatches("edge-1", "stream-uuid", matchingDesired) {
		t.Fatalf("matching snapshot must be reported verified")
	}

	// New desired source (e.g. operator updated bootstrap.yaml). If Mist
	// failed to apply, sidecar's snapshot still shows the OLD source —
	// must NOT be reported verified for the NEW desired snapshot.
	changedDesired := managedStreamSnapshot{
		sourceSpec: "ts-exec:cat /dev/zero",
		alwaysOn:   true,
		ingestMode: "mist_native",
	}
	if managedStreamVerifiedAppliedMatches("edge-1", "stream-uuid", changedDesired) {
		t.Fatalf("source-mismatch must NOT be reported verified — re-Apply must fire")
	}

	// Presence-only check (used for retract) still reports true: Mist has
	// SOME config for this stream, regardless of which version.
	if !managedStreamVerifiedAppliedPresent("edge-1", "stream-uuid") {
		t.Fatalf("presence-only check must report true while sidecar still tracks the stream")
	}
}

// TestIsManagedStreamEligibleNode_FiltersCapEdge pins the placement contract
// that mist_native placement requires CapEdge=true. Storage-only or
// processing-only nodes are healthy but cannot serve Mist playback or run a
// ts-exec input; electing one would never spawn the source.
func TestIsManagedStreamEligibleNode_FiltersCapEdge(t *testing.T) {
	allowed := map[string]struct{}{"media-eu-1": {}}
	cases := []struct {
		name string
		node *state.NodeState
		want bool
	}{
		{
			name: "edge_healthy_in_allowed",
			node: &state.NodeState{NodeID: "edge-1", ClusterID: "media-eu-1", IsHealthy: true, CapEdge: true},
			want: true,
		},
		{
			name: "storage_only_node_rejected",
			node: &state.NodeState{NodeID: "storage-1", ClusterID: "media-eu-1", IsHealthy: true, CapStorage: true, CapEdge: false},
			want: false,
		},
		{
			name: "processing_only_node_rejected",
			node: &state.NodeState{NodeID: "proc-1", ClusterID: "media-eu-1", IsHealthy: true, CapProcessing: true, CapEdge: false},
			want: false,
		},
		{
			name: "unhealthy_edge_rejected",
			node: &state.NodeState{NodeID: "edge-2", ClusterID: "media-eu-1", IsHealthy: false, CapEdge: true},
			want: false,
		},
		{
			name: "stale_edge_rejected",
			node: &state.NodeState{NodeID: "edge-3", ClusterID: "media-eu-1", IsHealthy: true, IsStale: true, CapEdge: true},
			want: false,
		},
		{
			name: "out_of_allowed_cluster_rejected",
			node: &state.NodeState{NodeID: "edge-4", ClusterID: "media-us-1", IsHealthy: true, CapEdge: true},
			want: false,
		},
		{
			name: "nil_node",
			node: nil,
			want: false,
		},
		{
			name: "blank_node_id",
			node: &state.NodeState{NodeID: "", ClusterID: "media-eu-1", IsHealthy: true, CapEdge: true},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isManagedStreamEligibleNode(tc.node, allowed); got != tc.want {
				t.Fatalf("isManagedStreamEligibleNode=%v want %v", got, tc.want)
			}
		})
	}
}

// TestEligibleNodesAcrossClusters_EmptyAllowedReturnsNil pins the contract
// that an empty source-cluster set yields no eligible nodes (bootstrap render
// requires one entry; the runtime defends against the empty case).
func TestEligibleNodesAcrossClusters_EmptyAllowedReturnsNil(t *testing.T) {
	got := eligibleNodesAcrossClusters(nil)
	if got != nil {
		t.Fatalf("want nil for empty allowed set, got %v", got)
	}
	got = eligibleNodesAcrossClusters([]string{})
	if got != nil {
		t.Fatalf("want nil for empty allowed set, got %v", got)
	}
}

func TestEligibleNodesAcrossClusters_RedisErrorIsTransient(t *testing.T) {
	store, mr := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "foghorn-a", "127.0.0.1:9000", nil))
	mr.Close()

	nodes, status := eligibleNodesAcrossClustersStatus([]string{"test-cluster"})
	if status != placementTransient {
		t.Fatalf("want transient placement status, got %v", status)
	}
	if nodes != nil {
		t.Fatalf("want no nodes on transient Redis failure, got %v", nodes)
	}
}

func TestManagedStreamOwnsConnection_RedisErrorIsTransient(t *testing.T) {
	store, mr := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "foghorn-a", "127.0.0.1:9000", nil))
	mr.Close()

	owns, status := managedStreamOwnsConnectionStatus("edge-1")
	if status != placementTransient {
		t.Fatalf("want transient ownership status, got %v", status)
	}
	if owns {
		t.Fatalf("must not claim ownership when Redis owner lookup fails")
	}
}

// TestPlacementPickWithCluster_AttributesClusterToElectedNode locks the
// post-election cluster attribution. Given an eligible (node, cluster) set, the
// elected entry MUST carry the actual cluster_id of the chosen node. The
// reconciler relies on this to admit and pin active_ingest_cluster_id at the
// elected node's real cluster instead of the reconciler's loop variable.
func TestPlacementPickWithCluster_AttributesClusterToElectedNode(t *testing.T) {
	// This helper is future-proof for cross-cluster eligible sets even though
	// mist_native bootstrap currently permits one source cluster.
	eligible := []eligibleNode{
		{nodeID: "edge-eu-1", clusterID: "eu"},
		{nodeID: "edge-eu-2", clusterID: "eu"},
		{nodeID: "edge-us-1", clusterID: "us"},
		{nodeID: "edge-us-2", clusterID: "us"},
	}
	got := placementPickWithCluster("stream-multi-region", eligible, 1)
	if len(got) != 1 {
		t.Fatalf("want one picked node, got %v", got)
	}
	// Whatever node is picked, its cluster attribution must match the
	// eligible-set entry for that node.
	for _, e := range eligible {
		if e.nodeID == got[0].nodeID {
			if e.clusterID != got[0].clusterID {
				t.Fatalf("elected cluster mismatch: node=%s want cluster=%s got=%s",
					got[0].nodeID, e.clusterID, got[0].clusterID)
			}
			return
		}
	}
	t.Fatalf("elected node %q not in eligible set", got[0].nodeID)
}

// TestPlacementPickWithCluster_DeterministicAcrossPeers asserts that two
// peer reconcilers seeing the same (sorted) eligible set + same stream_id
// converge on the same election — the precondition that lets ownership
// filtering decide which peer acts without coordination.
func TestPlacementPickWithCluster_DeterministicAcrossPeers(t *testing.T) {
	eligible := []eligibleNode{
		{nodeID: "edge-eu-1", clusterID: "eu"},
		{nodeID: "edge-eu-2", clusterID: "eu"},
		{nodeID: "edge-us-1", clusterID: "us"},
	}
	a := placementPickWithCluster("stream-x", eligible, 1)
	b := placementPickWithCluster("stream-x", eligible, 1)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("want 1 pick each, got a=%v b=%v", a, b)
	}
	if a[0] != b[0] {
		t.Fatalf("peer reconcilers disagreed on election: a=%v b=%v", a[0], b[0])
	}
}

// TestPlacementPickWithCluster_CountExceedsEligibleReturnsAll covers the
// clamp behavior for a placement_count greater than the eligible set.
// Same shape as placementPick's plain-strings variant.
func TestPlacementPickWithCluster_CountExceedsEligibleReturnsAll(t *testing.T) {
	eligible := []eligibleNode{
		{nodeID: "n-1", clusterID: "eu"},
		{nodeID: "n-2", clusterID: "us"},
	}
	got := placementPickWithCluster("any", eligible, 5)
	if len(got) != 2 {
		t.Fatalf("want all 2 when count > len, got %v", got)
	}
}

// TestManagedStreamSnapshot_ApplyKeyIgnoresInternalName locks the contract
// that internalName is carried for Retract use only and must NOT cause a
// false-positive Apply churn when only the captured-at-Apply name differs
// (which it never should for the same stream_id, but the apply gate must
// not depend on it).
func TestManagedStreamSnapshot_ApplyKeyIgnoresInternalName(t *testing.T) {
	a := managedStreamSnapshot{
		sourceSpec:   "ts-exec:cat /dev/null",
		alwaysOn:     true,
		ingestMode:   "mist_native",
		internalName: "internal-1",
	}
	b := managedStreamSnapshot{
		sourceSpec:   "ts-exec:cat /dev/null",
		alwaysOn:     true,
		ingestMode:   "mist_native",
		internalName: "",
	}
	if a.applyKey() != b.applyKey() {
		t.Fatalf("applyKey must ignore internalName")
	}
	if a == b {
		t.Fatalf("raw snapshot equality should still distinguish internalName")
	}
}

// TestManagedStreamSnapshot_ApplyKeyTracksSourceChanges asserts the apply
// gate fires when material fields change — a re-Apply with a different
// source must be sent even if the stream and node are the same.
func TestManagedStreamSnapshot_ApplyKeyTracksSourceChanges(t *testing.T) {
	a := managedStreamSnapshot{sourceSpec: "ts-exec:cat /dev/null", alwaysOn: true, ingestMode: "mist_native"}
	b := managedStreamSnapshot{sourceSpec: "ts-exec:cat /dev/zero", alwaysOn: true, ingestMode: "mist_native"}
	if a.applyKey() == b.applyKey() {
		t.Fatalf("source change must trigger a fresh Apply")
	}
}

// TestHydrateManagedStreamLastSentForNode_KeyedByStreamID locks the
// contract that hydration MUST key by stream_id to match the reconciler's
// own lastSent key. A hydration keyed by bare Mist name would cause the
// next tick to Apply (under stream_id) then Retract (the bare-name
// hydrated entry) the same physical stream — race against itself.
func TestHydrateManagedStreamLastSentForNode_KeyedByStreamID(t *testing.T) {
	managedStreamLastSent.Lock()
	managedStreamLastSent.m = make(map[string]map[string]map[string]managedStreamSnapshot)
	managedStreamLastSent.Unlock()
	t.Cleanup(func() {
		managedStreamLastSent.Lock()
		managedStreamLastSent.m = make(map[string]map[string]map[string]managedStreamSnapshot)
		managedStreamLastSent.Unlock()
	})

	HydrateManagedStreamLastSentForNode("edge-a", []*pb.AppliedManagedStream{
		{
			Name:       "frameworks-demo",
			Source:     "ts-exec:cat /dev/null",
			AlwaysOn:   true,
			IngestMode: "mist_native",
			StreamId:   "stream-uuid-1",
		},
		{
			// stream_id missing → must be skipped, not keyed under name.
			Name:       "legacy-stream",
			Source:     "ts-exec:cat /dev/zero",
			AlwaysOn:   true,
			IngestMode: "mist_native",
		},
	})

	managedStreamLastSent.Lock()
	defer managedStreamLastSent.Unlock()
	pending := managedStreamLastSent.m[managedStreamPendingClusterKey]
	if pending == nil {
		t.Fatalf("hydration must land under pending-cluster bucket")
	}
	nodeMap := pending["edge-a"]
	if nodeMap == nil {
		t.Fatalf("missing edge-a entries in pending")
	}
	if _, ok := nodeMap["stream-uuid-1"]; !ok {
		t.Fatalf("entry must be keyed by stream_id, got map keys %v", keysOf(nodeMap))
	}
	if _, ok := nodeMap["frameworks-demo"]; ok {
		t.Fatalf("entry must NOT be keyed by bare Mist name")
	}
	if _, ok := nodeMap["legacy-stream"]; ok {
		t.Fatalf("entry without stream_id must be skipped, not keyed by name")
	}
	if snap := nodeMap["stream-uuid-1"]; snap.internalName != "frameworks-demo" {
		t.Fatalf("internalName must be captured for Retract; got %q", snap.internalName)
	}
}

func keysOf(m map[string]managedStreamSnapshot) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestShouldRetractManagedStream covers the denied-vs-transient distinction
// that drives the retract loop. A previously-applied stream that goes
// denied (e.g. tenant suspended mid-stream) must be retracted; a stream
// whose admission lookup blipped transiently must NOT be retracted so a
// Commodore RPC error does not knock an always-on stream offline.
func TestShouldRetractManagedStream(t *testing.T) {
	admitted := map[string]struct{}{"admitted-sid": {}}
	transient := map[string]struct{}{"transient-sid": {}}

	cases := []struct {
		name        string
		streamID    string
		wantRetract bool
	}{
		{"admitted this tick → keep", "admitted-sid", false},
		{"transient lookup error → keep", "transient-sid", false},
		{"denied or no-longer-elected → retract", "denied-sid", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldRetractManagedStream(tc.streamID, admitted, transient)
			if got != tc.wantRetract {
				t.Fatalf("shouldRetractManagedStream(%q) = %v, want %v", tc.streamID, got, tc.wantRetract)
			}
		})
	}
}

// TestForgetManagedStreamLastSent_ScopedByCluster locks the per-cluster
// scoping fix: forgetting a node clears it from every cluster bucket but
// must not touch other nodes in those clusters.
func TestForgetManagedStreamLastSent(t *testing.T) {
	snap := func(src string) managedStreamSnapshot {
		return managedStreamSnapshot{sourceSpec: src, alwaysOn: true, ingestMode: "mist_native"}
	}
	managedStreamLastSent.Lock()
	managedStreamLastSent.m["cluster-a"] = map[string]map[string]managedStreamSnapshot{
		"edge-1": {"stream-x": snap("ts-exec:cat /dev/null")},
		"edge-2": {"stream-y": snap("ts-exec:cat /dev/null")},
	}
	managedStreamLastSent.m["cluster-b"] = map[string]map[string]managedStreamSnapshot{
		"edge-1": {"stream-z": snap("ts-exec:cat /dev/zero")},
	}
	managedStreamLastSent.Unlock()

	ForgetManagedStreamLastSent("edge-1")

	managedStreamLastSent.Lock()
	defer managedStreamLastSent.Unlock()
	if _, present := managedStreamLastSent.m["cluster-a"]["edge-1"]; present {
		t.Fatalf("edge-1 in cluster-a not forgotten")
	}
	if _, present := managedStreamLastSent.m["cluster-a"]["edge-2"]; !present {
		t.Fatalf("edge-2 in cluster-a incorrectly forgotten")
	}
	// cluster-b only had edge-1; should now be empty AND cluster-b key dropped.
	if _, present := managedStreamLastSent.m["cluster-b"]; present {
		t.Fatalf("empty cluster-b bucket should have been dropped")
	}
	// Cleanup so the package-level state doesn't leak into other tests.
	delete(managedStreamLastSent.m, "cluster-a")
}
