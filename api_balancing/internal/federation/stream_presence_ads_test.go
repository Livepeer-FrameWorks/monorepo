package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// The origin side of these tests matches livePathFixture's receiving side: the
// stream "internal" of tenant "tenant" is published on node "eu-publisher" in
// cluster "eu-ingest", whose Foghorn answers for control cell "eu-cell".
const (
	presenceStream     = "internal"
	presenceTenant     = "tenant"
	presenceNode       = "eu-publisher"
	presenceCluster    = "eu-ingest"
	presenceCell       = "eu-cell"
	presenceGeneration = "source-generation"
	presenceRevision   = int64(9007199254740993)
	// presenceWait is far below streamAdPushInterval: an ad arriving within it
	// did not come from the periodic push, which these tests never run anyway.
	presenceWait = time.Second
)

// presencePeer attaches a connected, tenant-authorized peer with a mailbox, so
// enqueued frames can be read without a writer goroutine.
func presencePeer(pm *PeerManager, peerID string) chan *foghornfederationpb.PeerMessage {
	ch := make(chan *foghornfederationpb.PeerMessage, peerSendQueueSize)
	pm.mu.Lock()
	pm.peers[peerID] = &peerState{connected: true, stream: &capturePeerChannelStream{}, sendCh: ch,
		lifecycle: peerAlwaysOn, tenantIDs: []string{presenceTenant}}
	pm.mu.Unlock()
	return ch
}

func presenceLeader(t *testing.T, sm *state.StreamStateManager, registry *control.StreamRegistry) chan *foghornfederationpb.PeerMessage {
	t.Helper()
	pm := newTestPeerManager(t, presenceCluster, nil, true)
	pm.controlCellID = presenceCell
	ch := presencePeer(pm, "us-peer")
	t.Cleanup(pm.WatchStreamPresence(sm, registry))
	return ch
}

// seedPresenceOrigin brings the publisher's instance up with an input and a
// DTSC listener but not yet playable (EMPTY buffer).
func seedPresenceOrigin(t *testing.T, sm *state.StreamStateManager) {
	t.Helper()
	seedFederationNodeAndStream(t, sm, presenceNode, presenceStream, presenceTenant)
	sm.SetNodeConnectionInfo(context.Background(), presenceNode, "", presenceTenant, presenceCluster, nil)
	sm.SetNodeInfo(presenceNode, "https://eu.example", true, nil, nil, "", "", map[string]any{"DTSC": "dtsc://HOST:14200/$"})
	if err := sm.UpdateStreamFromBuffer(presenceStream, presenceStream, presenceNode, presenceTenant, "EMPTY", ""); err != nil {
		t.Fatal(err)
	}
}

func confirmPresenceBinding(t *testing.T, registry *control.StreamRegistry) {
	t.Helper()
	registry.UpsertLocalSource(control.StreamEntry{InternalName: presenceStream, TenantID: presenceTenant, IngestMode: control.IngestPush})
	if _, applied, err := registry.ProjectSource(presenceStream, presenceNode, 1, "trigger", presenceGeneration, presenceRevision); err != nil || !applied {
		t.Fatalf("project source: %v %v", applied, err)
	}
}

func flipPresencePlayable(t *testing.T, sm *state.StreamStateManager) {
	t.Helper()
	if err := sm.UpdateStreamFromBuffer(presenceStream, presenceStream, presenceNode, presenceTenant, "FULL", ""); err != nil {
		t.Fatal(err)
	}
}

func awaitPresenceAd(t *testing.T, ch chan *foghornfederationpb.PeerMessage) *foghornfederationpb.StreamAdvertisement {
	t.Helper()
	select {
	case msg := <-ch:
		ad := msg.GetStreamAd()
		if ad == nil {
			t.Fatalf("expected a stream advertisement, got %#v", msg.GetPayload())
		}
		return ad
	case <-time.After(presenceWait):
		t.Fatalf("no stream advertisement within %s of the origin becoming present", presenceWait)
		return nil
	}
}

func assertPresentOriginAd(t *testing.T, ad *foghornfederationpb.StreamAdvertisement) {
	t.Helper()
	if ad.GetInternalName() != presenceStream || ad.GetTenantId() != presenceTenant || ad.GetControlCellId() != presenceCell || !ad.GetIsLive() {
		t.Fatalf("advertisement identity: %+v", ad)
	}
	if len(ad.GetEdges()) != 1 {
		t.Fatalf("advertisement edges: %+v", ad.GetEdges())
	}
	edge := ad.GetEdges()[0]
	if edge.GetNodeId() != presenceNode || edge.GetClusterId() != presenceCluster || !edge.GetIsOrigin() || !edge.GetPlayable() ||
		edge.GetSourceGeneration() != presenceGeneration || edge.GetSourceRevision() != presenceRevision || edge.GetSourceObservedAt() == 0 {
		t.Fatalf("origin edge does not carry placement-present publisher evidence: %+v", edge)
	}
}

func installPresenceRegistry(t *testing.T, registry *control.StreamRegistry) {
	t.Helper()
	previous := control.StreamRegistryInstance
	control.StreamRegistryInstance = registry
	t.Cleanup(func() { control.StreamRegistryInstance = previous })
}

// assertPeerResolvesPresenceGeneration applies the ad on a receiving cell
// exactly as its PeerChannel handler does and asks that cell's push placement
// reader for the current source generation.
func assertPeerResolvesPresenceGeneration(t *testing.T, ad *foghornfederationpb.StreamAdvertisement) {
	t.Helper()
	f, reader, _ := livePathFixture(t)
	receiver := control.NewStreamRegistry(nil, "us", time.Minute)
	installPresenceRegistry(t, receiver)
	reader.Registry = receiver
	reader.Now = time.Now
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	if generation, _, resolveErr := reader.ResolveSourceGeneration(context.Background(), authority); resolveErr == nil {
		t.Fatalf("receiving cell resolved %q before any advertisement arrived", generation)
	}
	if !applyRegistryStreamAdvertisement(ad) {
		t.Fatal("receiving cell rejected the advertisement")
	}
	generation, _, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil || generation != presenceGeneration {
		t.Fatalf("receiving cell source generation = %q, %v; want %q", generation, err, presenceGeneration)
	}
}

func TestStreamPresenceAdvertisesWhenOriginBecomesPlayable(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	registry := control.NewStreamRegistry(nil, presenceCluster, time.Minute)
	installPresenceRegistry(t, registry)
	leaderPeer := presenceLeader(t, sm, registry)

	follower := newTestPeerManager(t, presenceCluster, nil, false)
	follower.controlCellID = presenceCell
	followerPeer := presencePeer(follower, "us-peer")
	t.Cleanup(follower.WatchStreamPresence(sm, registry))

	seedPresenceOrigin(t, sm)
	confirmPresenceBinding(t, registry)
	flipPresencePlayable(t, sm)

	// Nothing was present before the flip, so the first frame is the one the
	// flip caused.
	ad := awaitPresenceAd(t, leaderPeer)
	assertPresentOriginAd(t, ad)

	// Only the leader holds PeerChannels in production; a follower with a peer
	// attached still must not send.
	select {
	case msg := <-followerPeer:
		t.Fatalf("non-leader replica advertised: %#v", msg.GetPayload())
	case <-time.After(300 * time.Millisecond):
	}

	// Further writes that leave the same origin present are coalesced into the
	// advertisement already sent.
	flipPresencePlayable(t, sm)
	sm.UpdateNodeStats(presenceStream, presenceNode, 12, 1, 0, 0, false)
	select {
	case msg := <-leaderPeer:
		t.Fatalf("unchanged presence was advertised again: %#v", msg.GetPayload())
	case <-time.After(300 * time.Millisecond):
	}

	assertPeerResolvesPresenceGeneration(t, ad)
}

func TestStreamPresenceAdvertisesWhenBindingConfirmedWhilePlayable(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	registry := control.NewStreamRegistry(nil, presenceCluster, time.Minute)
	installPresenceRegistry(t, registry)
	leaderPeer := presenceLeader(t, sm, registry)

	seedPresenceOrigin(t, sm)
	flipPresencePlayable(t, sm)
	confirmPresenceBinding(t, registry)

	assertPresentOriginAd(t, awaitPresenceAd(t, leaderPeer))
}

// The trigger lands on a non-leader replica: its state and registry writes
// reach the leader only through the cell's changelogs, and the leader must
// advertise from what it applies. Either write can be the one that completes
// presence, so both orders are covered.
func TestStreamPresenceAdvertisesChangesMadeOnAnotherReplica(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps func(t *testing.T, sm *state.StreamStateManager, registry *control.StreamRegistry)
	}{
		{"playable_last", func(t *testing.T, sm *state.StreamStateManager, registry *control.StreamRegistry) {
			confirmPresenceBinding(t, registry)
			flipPresencePlayable(t, sm)
		}},
		{"binding_last", func(t *testing.T, sm *state.StreamStateManager, registry *control.StreamRegistry) {
			flipPresencePlayable(t, sm)
			confirmPresenceBinding(t, registry)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
			logger := logging.NewLogger()

			replicaState := func(instanceID string) *state.StreamStateManager {
				sm := state.NewStreamStateManager()
				if err := sm.EnableRedisSync(context.Background(), state.NewRedisStateStore(client, presenceCluster), instanceID, logger); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(sm.Shutdown)
				return sm
			}
			replicaRegistry := func(instanceID string) *control.StreamRegistry {
				r := control.NewStreamRegistry(nil, presenceCluster, time.Minute)
				if _, _, err := r.EnableRedisSync(context.Background(), control.NewRedisRegistryStore(client, presenceCluster), instanceID, logger); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(r.DisableRedisSync)
				return r
			}
			leaderState, leaderRegistry := replicaState("leader"), replicaRegistry("leader")
			writerState, writerRegistry := replicaState("writer"), replicaRegistry("writer")
			// Cleanups run last-in first-out: closing the client first ends the
			// changelog readers' blocking reads instead of waiting out their block.
			t.Cleanup(func() { _ = client.Close() })
			leaderPeer := presenceLeader(t, leaderState, leaderRegistry)

			seedPresenceOrigin(t, writerState)
			tc.steps(t, writerState, writerRegistry)

			ad := awaitPresenceAd(t, leaderPeer)
			assertPresentOriginAd(t, ad)
			assertPeerResolvesPresenceGeneration(t, ad)
		})
	}
}
