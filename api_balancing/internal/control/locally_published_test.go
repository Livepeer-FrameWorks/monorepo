package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// installSessionClaim gives the stream an open ingest session carrying the
// placement claim it was admitted under. Renewal replays that claim rather than
// re-resolving the node's current cluster, so without it there is nothing to
// re-assert and the stream is not reported.
func installSessionClaim(t *testing.T, clusterID, claimToken string) {
	t.Helper()
	installSessionClaimForNode(t, "edge-node-1", clusterID, claimToken)
}

// installSessionClaimForNode is the same, with the session owned by a specific
// node — renewal matches the claim to the registry's owning node, not just to
// the stream.
func installSessionClaimForNode(t *testing.T, nodeID, clusterID, claimToken string) {
	t.Helper()
	claimDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.MatchExpectationsInOrder(false)
	rows := sqlmock.NewRows([]string{"tenant_id", "stream_internal_name", "node_id", "ingest_cluster_id", "start_trigger_uuid", "id"})
	if clusterID != "" {
		rows.AddRow("tenant-1", publishedInternalName, nodeID, clusterID, claimToken, seededGeneration)
	}
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnRows(rows)
	SetDB(claimDB)
	t.Cleanup(func() { SetDB(nil); _ = claimDB.Close() })
}

const (
	publishedInternalName = "60546679b497415db2338cd5cae54992"
	// The projection's connection identity. Renewal only replays a claim whose
	// session matches the projection on node, connection, AND generation.
	seededTriggerUUID = "trigger-uuid-1"
	seededGeneration  = "gen-seed"
)

func registryWithLivePublisher(t *testing.T, nodeID string) *StreamRegistry {
	t.Helper()
	prev := StreamRegistryInstance
	r := NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	if _, err := r.ResolveSourceByInternalName(context.Background(), publishedInternalName); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	projectSourceForTest(t, r, publishedInternalName, nodeID, 4242, seededTriggerUUID, seededGeneration, 1)
	StreamRegistryInstance = r
	t.Cleanup(func() { StreamRegistryInstance = prev })
	holdControlConnection(t, nodeID)
	return r
}

// holdControlConnection makes this process the owner of the node's control
// stream. Renewal is sharded on that ownership, so tests that expect a
// publisher to be reported must hold its node's connection.
func holdControlConnection(t *testing.T, nodeIDs ...string) {
	t.Helper()
	prev := registry
	registry = &Registry{conns: make(map[string]*conn), log: logging.NewLogger()}
	for _, id := range nodeIDs {
		registry.conns[id] = &conn{}
	}
	t.Cleanup(func() { registry = prev })
}

func mustLocallyPublishedStreams(t *testing.T) []LocallyPublishedStream {
	t.Helper()
	live, err := LocallyPublishedStreams(context.Background())
	if err != nil {
		t.Fatalf("list locally published streams: %v", err)
	}
	return live
}

// The owning node is what renewal needs: placement was claimed under the
// cluster resolved for that node, and the caller re-resolves it the same way.
func TestLocallyPublishedStreams_ReportsPublishingNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	registryWithLivePublisher(t, "edge-node-1")
	installSessionClaim(t, "demo-media", seededTriggerUUID)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	sm.TouchNode("edge-node-1", true)

	live := mustLocallyPublishedStreams(t)
	if len(live) != 1 {
		t.Fatalf("expected one live publisher, got %+v", live)
	}
	if live[0].OwnerNodeID != "edge-node-1" {
		t.Fatalf("owner node = %q, want edge-node-1", live[0].OwnerNodeID)
	}
	// The claim replayed is the session's, not the node's current registration.
	if live[0].ClusterID != "demo-media" || live[0].ClaimToken != seededTriggerUUID {
		t.Fatalf("claim not replayed from the session: %+v", live[0])
	}
	if live[0].InternalName != publishedInternalName || live[0].TenantID == "" {
		t.Fatalf("unattributed live publisher: %+v", live[0])
	}
}

// The stream registry is synchronized across Foghorn replicas, so a publisher
// projected by one is visible to all. Renewal follows the owner node's control
// connection, which lives on exactly one replica: a replica that does not hold
// it must leave the claim to the one that does, or every replica renews the
// whole fleet and no per-replica budget bounds anything.
func TestLocallyPublishedStreams_ExcludesNodesConnectedToAnotherReplica(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	registryWithLivePublisher(t, "edge-node-1")
	installSessionClaim(t, "demo-media", seededTriggerUUID)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	sm.TouchNode("edge-node-1", true)

	if len(mustLocallyPublishedStreams(t)) != 1 {
		t.Fatal("seed publisher was not reported live while this replica held its connection")
	}

	// The node's control stream now lives on another replica. Its state stays
	// healthy here — the shared state manager is not what partitions renewal.
	holdControlConnection(t, "edge-node-2")
	installSessionClaim(t, "demo-media", seededTriggerUUID)

	if live := mustLocallyPublishedStreams(t); len(live) != 0 {
		t.Fatalf("a publisher owned by another replica's connection was renewed here: %+v", live)
	}
}

// A source that is no longer active is not a live publisher: renewing it would
// hold placement against a publisher that has gone, and no other cluster could
// take it.
func TestLocallyPublishedStreams_ExcludesClosedSources(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	r := registryWithLivePublisher(t, "edge-node-1")
	installSessionClaim(t, "demo-media", seededTriggerUUID)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	sm.TouchNode("edge-node-1", true)

	if !markSourceInactiveForTest(t, r, publishedInternalName, "edge-node-1", "", 2) {
		t.Fatal("seed close did not flip the source inactive")
	}

	if live := mustLocallyPublishedStreams(t); len(live) != 0 {
		t.Fatalf("a closed publisher is still reported live: %+v", live)
	}
}

// A Location whose owning node this Foghorn has no state for belongs to a
// federated peer. Renewing it would claim placement in a cluster whose
// publishers are that peer's to account for.
func TestLocallyPublishedStreams_ExcludesUnownedNodes(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	registryWithLivePublisher(t, "peer-node-9")
	installSessionClaim(t, "demo-media", seededTriggerUUID)

	if live := mustLocallyPublishedStreams(t); len(live) != 0 {
		t.Fatalf("a publisher on an unknown node was reported: %+v", live)
	}
}

// A node record outlives a disconnect: it stays present, first unhealthy and
// later stale, while a source-active Location is deliberately non-evictable
// and survives in Redis without a TTL. Reporting such a publisher as live
// would renew placement for one that has crashed, and no other cluster could
// then take the claim.
func TestLocallyPublishedStreams_ExcludesUnhealthyAndStaleOwners(t *testing.T) {
	for _, tc := range []struct {
		name    string
		degrade func(*state.StreamStateManager)
	}{
		{
			name:    "owner unhealthy",
			degrade: func(sm *state.StreamStateManager) { sm.TouchNode("edge-node-1", false) },
		},
		{
			name:    "owner disconnected",
			degrade: func(sm *state.StreamStateManager) { sm.MarkNodeDisconnected("edge-node-1") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(func() { state.ResetDefaultManagerForTests() })
			registryWithLivePublisher(t, "edge-node-1")
			installSessionClaim(t, "demo-media", seededTriggerUUID)
			sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
			sm.TouchNode("edge-node-1", true)

			if len(mustLocallyPublishedStreams(t)) != 1 {
				t.Fatal("seed publisher was not reported live")
			}
			tc.degrade(sm)
			installSessionClaim(t, "demo-media", seededTriggerUUID)

			if live := mustLocallyPublishedStreams(t); len(live) != 0 {
				t.Fatalf("a publisher on a %s node is still reported live: %+v", tc.name, live)
			}
		})
	}
}

// A node reassigned to another virtual media cluster mid-session must not
// change which claim is re-asserted: the claim was taken under the cluster the
// session was admitted into, and asserting a different one would leave the
// original to lapse under a publisher that is still connected.
func TestLocallyPublishedStreams_ReplaysAdmittedClusterAcrossReassignment(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	registryWithLivePublisher(t, "edge-node-1")
	installSessionClaim(t, "demo-media", seededTriggerUUID)
	// The node now reports a different cluster than the session was admitted into.
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "other-media", nil)
	sm.TouchNode("edge-node-1", true)

	live := mustLocallyPublishedStreams(t)
	if len(live) != 1 {
		t.Fatalf("expected one live publisher, got %+v", live)
	}
	if live[0].ClusterID != "demo-media" {
		t.Fatalf("cluster = %q, want the admitted demo-media, not the node's current registration", live[0].ClusterID)
	}
}

// A source-active stream with no open session carrying a claim has nothing to
// re-assert. Guessing a cluster for it is exactly the defect the session
// binding replaced.
func TestLocallyPublishedStreams_SkipsStreamsWithoutASessionClaim(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	registryWithLivePublisher(t, "edge-node-1")
	installSessionClaim(t, "", "")
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	sm.TouchNode("edge-node-1", true)

	if live := mustLocallyPublishedStreams(t); len(live) != 0 {
		t.Fatalf("a stream with no session claim was reported: %+v", live)
	}
}

// The registry is a projection and can drift. A source-active entry naming a
// node that has no open session must not borrow another session's claim: doing
// so would keep an unrelated publisher's placement alive for as long as the
// drifted node stayed healthy.
func TestLocallyPublishedStreams_RequiresTheClaimToBelongToTheOwningNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	// The registry says edge-node-1 owns the source...
	registryWithLivePublisher(t, "edge-node-1")
	// ...while the only open session for this stream belongs to another node.
	installSessionClaimForNode(t, "edge-node-2", "demo-media", seededTriggerUUID)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	sm.TouchNode("edge-node-1", true)

	if live := mustLocallyPublishedStreams(t); len(live) != 0 {
		t.Fatalf("a claim from another node's session was re-asserted: %+v", live)
	}
}

func TestLocallyPublishedStreams_ReturnsSessionEnumerationFailure(t *testing.T) {
	prevRegistry := StreamRegistryInstance
	StreamRegistryInstance = NewStreamRegistry(&fakeCommodore{resp: nativeResp()}, "cluster-A", time.Minute)
	t.Cleanup(func() { StreamRegistryInstance = prevRegistry })
	claimDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.ExpectQuery(`FROM foghorn\.ingest_sessions`).WillReturnError(context.DeadlineExceeded)
	SetDB(claimDB)
	t.Cleanup(func() { SetDB(nil); _ = claimDB.Close() })

	if _, err := LocallyPublishedStreams(context.Background()); err == nil {
		t.Fatal("session enumeration failure was hidden as an empty publisher set")
	}
}
