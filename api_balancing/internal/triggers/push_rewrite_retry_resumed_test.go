package triggers

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// liveRecordingPeerNotifier counts LIVE lifecycle broadcasts — an effect owned by the durable
// admission obligation, which the trigger goroutine must never send inline.
type liveRecordingPeerNotifier struct {
	mu         sync.Mutex
	live       int
	nonLeader  bool
	trackStale bool
	tracked    [][]string // each TrackStream call's cluster IDs
}

func (l *liveRecordingPeerNotifier) TrackStream(_ context.Context, _, _, _ string, _ int64, peerHints []control.AdmissionPeerHint) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	clusterIDs := make([]string, 0, len(peerHints))
	for _, hint := range peerHints {
		clusterIDs = append(clusterIDs, hint.ClusterID)
	}
	l.tracked = append(l.tracked, clusterIDs)
	return !l.trackStale, nil
}
func (l *liveRecordingPeerNotifier) UntrackStream(_ context.Context, _, _, _ string, _ int64) error {
	return nil
}
func (l *liveRecordingPeerNotifier) BroadcastStreamLifecycle(_ context.Context, _, _ string, _ int64, isLive bool) error {
	if !isLive {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.live++
	return nil
}

func (l *liveRecordingPeerNotifier) IsStreamLiveOnPeer(_ context.Context, _, _ string) (string, bool) {
	return "", false
}

// notLeader simulates a replica without PeerManager leadership.
func (l *liveRecordingPeerNotifier) IsLeader() bool { return !l.nonLeader }

func (l *liveRecordingPeerNotifier) LeaderInstanceID() string { return "leader-instance" }

func (l *liveRecordingPeerNotifier) liveCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live
}

// A PUSH_REWRITE retry whose session is already projected and confirmed (the first accept response
// was lost) must return the IDENTICAL accept. Every once-only admission effect — Decklog ingest
// event, federation live broadcast, push activation, prior-owner drain — is owned by the durable
// obligation the FIRST confirmation persisted (unique per generation), so the retry sends nothing
// inline and enqueues nothing new. Drives the real handlePushRewrite against the resumed-session DB
// state; fails if the retry is denied, the response differs, or an effect leaks back inline.
func TestPushRewriteRetry_ResumedProjectionAcceptsIdentically(t *testing.T) {
	const internalName = "resumed-internal-name"
	installIngestSessionResumedMock(t, internalName, 4242)

	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	commodoreClient, cleanup := setupCommodoreClient(t, &commodorepb.ValidateStreamKeyResponse{
		Valid:        true,
		UserId:       "user-1",
		TenantId:     "tenant-1",
		InternalName: internalName,
		StreamId:     "stream-1",
		BillingModel: "postpaid",
	}, nil)
	t.Cleanup(cleanup)

	capture, client := startDecklogCapture(t)
	peer := &liveRecordingPeerNotifier{}

	p := newTestProcessor(t)
	p.commodoreClient = commodoreClient
	p.decklogClient = client
	p.clusterID = "cluster-local"
	p.SetPeerNotifier(peer)

	streamName, blocking, err := p.handlePushRewrite(&ipcpb.MistTrigger{
		NodeId: "edge-node-1",
		TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
			PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "retry-trigger-uuid", TriggerUnixMillis: 1,
				StreamName: "sk_live_retry_key",
				PushUrl:    "rtmp://edge-ingest.example.com:1935/live/sk_live_retry_key",
				Hostname:   "203.0.113.9",
			},
		},
	})
	if err != nil {
		t.Fatalf("a retry of an accepted push must be re-accepted, got error: %v", err)
	}
	if blocking || streamName != "live+"+internalName {
		t.Fatalf("retry response = (%q, blocking=%v), want the identical accept live+%s", streamName, blocking, internalName)
	}

	// EVERY once-only effect — including the Decklog ingest event — is owned by the durable
	// obligation the FIRST confirmation persisted; the retry sends nothing inline and enqueues
	// nothing new (the obligation is unique per generation).
	time.Sleep(150 * time.Millisecond)
	if got := len(capture.received()); got != 0 {
		t.Fatalf("the retry sent %d Decklog event(s) inline, want 0 (obligation-owned)", got)
	}
	if got := peer.liveCount(); got != 0 {
		t.Fatalf("the trigger goroutine broadcast federation live %d time(s), want 0 (obligation-owned)", got)
	}
}

// ApplyAdmissionEffect drives owed legs: the drain leg zeroes the replaced owner's balancer input
// and DISPATCHES the drain (completion is the Helmsman ack, so the leg is not reported done here);
// the broadcast leg completes locally and is reported done; legs already done are not re-run.
func TestApplyAdmissionEffect_DrainsPriorOwnerAndBroadcasts(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	const internal, newOwner, oldOwner, tenant = "apply-admission", "node-new", "node-old", "tenant-1"
	sm.SetStreamInstanceInputs(internal, oldOwner, 1)

	peer := &liveRecordingPeerNotifier{}
	p := minimalProcessorForStreamEnd(t)
	p.SetPeerNotifier(peer)
	p.nodeOwnedLocally = func(string) bool { return true }
	var drained []string
	p.drainStream = func(_ context.Context, nodeID, runtimeName, reason, sourceGeneration, priorOwnerSourceGeneration string) error {
		drained = append(drained, nodeID+"|"+runtimeName+"|"+reason+"|"+sourceGeneration)
		return nil
	}

	legs, err := p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID:         tenant,
		InternalName:     internal,
		NodeID:           newOwner,
		SourceGeneration: "gen-1",
		PriorOwnerNodeID: oldOwner,
		BroadcastLive:    true,
		PeerHints: []control.AdmissionPeerHint{
			{ClusterID: "peer-cluster-1", Addr: "peer-1:18019"},
			{ClusterID: "peer-cluster-2", Addr: "peer-2:18019"},
		},
	})
	if err != nil {
		t.Fatalf("ApplyAdmissionEffect: %v", err)
	}
	// The leader must (re-)establish stream tracking from the DURABLE peer set before the
	// broadcast — its process-local tracking cannot be assumed to know the stream.
	peer.mu.Lock()
	tracked := peer.tracked
	peer.mu.Unlock()
	if len(tracked) != 1 || len(tracked[0]) != 2 || tracked[0][0] != "peer-cluster-1" {
		t.Fatalf("leader must track the persisted peer clusters before broadcasting, got %v", tracked)
	}

	if inst, present := sm.GetStreamInstances(internal)[oldOwner]; present && inst.Inputs != 0 {
		t.Fatalf("prior owner's balancer input must be zeroed, got %+v", inst)
	}
	if len(drained) != 1 {
		t.Fatalf("prior owner drain dispatched %d time(s), want 1: %v", len(drained), drained)
	}
	if got := peer.liveCount(); got != 1 {
		t.Fatalf("federation live broadcast sent %d time(s), want 1", got)
	}
	// The broadcast completes locally; the drain is dispatch-only (the Helmsman ack completes it).
	if !legs.BroadcastDone {
		t.Fatal("the broadcast leg must be reported done")
	}

	// Legs already done (or absent) are not re-run.
	drained = nil
	legs, err = p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID: tenant, InternalName: internal, NodeID: newOwner, SourceGeneration: "gen-2",
		DrainDone: true, BroadcastDone: true, DecklogDone: true, ActivationDone: true,
	})
	if err != nil {
		t.Fatalf("ApplyAdmissionEffect (all done): %v", err)
	}
	if len(drained) != 0 || peer.liveCount() != 1 || legs.BroadcastDone {
		t.Fatalf("done legs must be no-ops (drained=%v broadcasts=%d legs=%+v)", drained, peer.liveCount(), legs)
	}
}

// A claimant that does not own the publishing node's control connection defers activation, while
// replica-independent drain dispatch can still progress. Federation broadcast has its own leader
// authority check.
func TestApplyAdmissionEffect_NotOwnerSkipsNodeAffineLegs(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	peer := &liveRecordingPeerNotifier{}
	p := minimalProcessorForStreamEnd(t)
	p.SetPeerNotifier(peer)
	p.nodeOwnedLocally = func(string) bool { return false }
	var drained []string
	p.drainStream = func(_ context.Context, nodeID, runtimeName, reason, sourceGeneration, priorOwnerSourceGeneration string) error {
		drained = append(drained, nodeID)
		return nil
	}

	// Activation requires PushTargets to be owed; give the obligation one so the node-affinity
	// gate is exercised (the broadcast is separately leader-gated below).
	legs, err := p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID:         "tenant-1",
		InternalName:     "not-owner-stream",
		NodeID:           "node-elsewhere",
		SourceGeneration: "gen-1",
		PriorOwnerNodeID: "node-old",
		PushTargets:      []byte{0x01}, // owed activation leg (decodability is not reached: deferred first)
		BroadcastDone:    true,
		DecklogDone:      true,
	})
	if err != nil {
		t.Fatalf("ApplyAdmissionEffect: %v", err)
	}
	if !legs.Deferred {
		t.Fatal("a claimant without node-connection ownership must defer the activation leg")
	}
	if len(drained) != 1 {
		t.Fatalf("the replica-agnostic drain dispatch must still progress, got %v", drained)
	}
}

// The federation broadcast leg is LEADER-owned: only the PeerManager leader has open peer channels,
// so a non-leader must defer rather than broadcast to zero peers and mark the leg done.
func TestApplyAdmissionEffect_NonLeaderDefersBroadcast(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	prevRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(prevRegistry) })

	peer := &liveRecordingPeerNotifier{nonLeader: true}
	p := minimalProcessorForStreamEnd(t)
	p.SetPeerNotifier(peer)
	p.nodeOwnedLocally = func(string) bool { return true }

	legs, err := p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID:         "tenant-1",
		InternalName:     "nonleader-stream",
		NodeID:           "node-here",
		SourceGeneration: "gen-1",
		BroadcastLive:    true,
		DrainDone:        true,
		DecklogDone:      true,
	})
	if err != nil {
		t.Fatalf("ApplyAdmissionEffect: %v", err)
	}
	if legs.BroadcastDone || !legs.Deferred || peer.liveCount() != 0 {
		t.Fatalf("non-leader must defer the broadcast (done=%v deferred=%v count=%d)", legs.BroadcastDone, legs.Deferred, peer.liveCount())
	}
}

func TestApplyAdmissionEffect_StaleMembershipDoesNotBroadcastLive(t *testing.T) {
	peer := &liveRecordingPeerNotifier{trackStale: true}
	p := minimalProcessorForStreamEnd(t)
	p.SetPeerNotifier(peer)

	legs, err := p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID:         "tenant-1",
		InternalName:     "superseded-stream",
		SourceGeneration: "generation-a",
		SourceRevision:   41,
		BroadcastLive:    true,
		DrainDone:        true,
		ActivationDone:   true,
		DecklogDone:      true,
		PeerHints: []control.AdmissionPeerHint{{
			ClusterID: "peer-a",
			Addr:      "peer-a:18019",
		}},
	})
	if err != nil {
		t.Fatalf("ApplyAdmissionEffect: %v", err)
	}
	if legs.BroadcastDone || peer.liveCount() != 0 {
		t.Fatalf("stale membership emitted live lifecycle: legs=%+v broadcasts=%d", legs, peer.liveCount())
	}
	peer.mu.Lock()
	trackCalls := len(peer.tracked)
	peer.mu.Unlock()
	if trackCalls != 1 {
		t.Fatalf("TrackStream calls = %d, want 1", trackCalls)
	}
}

func TestApplyAdmissionEffect_InvalidPeerHintsPoisonOnlyBroadcast(t *testing.T) {
	peer := &liveRecordingPeerNotifier{}
	p := minimalProcessorForStreamEnd(t)
	p.SetPeerNotifier(peer)

	legs, err := p.ApplyAdmissionEffect(context.Background(), control.AdmissionEffect{
		TenantID:         "tenant-1",
		InternalName:     "invalid-peer-hints",
		SourceGeneration: "gen-1",
		BroadcastLive:    true,
		PeerHintsInvalid: true,
		DrainDone:        true,
		ActivationDone:   true,
		DecklogDone:      true,
	})
	if err != nil {
		t.Fatalf("ApplyAdmissionEffect: %v", err)
	}
	if !legs.BroadcastPoisoned || legs.BroadcastDone || peer.liveCount() != 0 {
		t.Fatalf("invalid durable peer hints were not isolated to broadcast poison: legs=%+v broadcasts=%d", legs, peer.liveCount())
	}
	peer.mu.Lock()
	tracked := len(peer.tracked)
	peer.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("invalid durable peer hints reached TrackStream %d time(s)", tracked)
	}
}
