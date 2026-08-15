package triggers

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// recordingPeerNotifier records the federation offline effects (an offline BroadcastStreamLifecycle
// and UntrackStream) so a test can assert they did NOT run for a live reconnect.
type recordingPeerNotifier struct {
	mu               sync.Mutex
	offlineBroadcast []string
	untracked        []string
}

func (r *recordingPeerNotifier) TrackStream(_ context.Context, _, _, _ string, _ int64, _ []control.AdmissionPeerHint) (bool, error) {
	return true, nil
}
func (r *recordingPeerNotifier) UntrackStream(_ context.Context, name, _, _ string, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.untracked = append(r.untracked, name)
	return nil
}

func (r *recordingPeerNotifier) BroadcastStreamLifecycle(_ context.Context, internalName, _ string, _ int64, isLive bool) error {
	if isLive {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offlineBroadcast = append(r.offlineBroadcast, internalName)
	return nil
}

func (r *recordingPeerNotifier) IsStreamLiveOnPeer(_ context.Context, _, _ string) (string, bool) {
	return "", false
}

func (r *recordingPeerNotifier) IsLeader() bool { return true }

func (r *recordingPeerNotifier) LeaderInstanceID() string { return "leader-instance" }

func (r *recordingPeerNotifier) counts() (offline, untrack int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.offlineBroadcast), len(r.untracked)
}

// streamEndTriggersSeen counts how many STREAM_END triggers the Decklog capture received.
func streamEndTriggersSeen(triggers []*ipcpb.MistTrigger) int {
	n := 0
	for _, tr := range triggers {
		if tr.GetStreamEnd() != nil {
			n++
		}
	}
	return n
}

// offlineObservers bundles every stream-wide offline effect this package drives, so a suppression
// test can assert on ALL of them (not just registry + inputs): per-node routing input, Decklog
// forwarding, tenant capacity, push-target tracking, and federation broadcast/untrack.
type offlineObservers struct {
	reg      *control.StreamRegistry
	sm       *state.StreamStateManager
	capacity *state.TenantCapacityManager
	peer     *recordingPeerNotifier
	capture  *decklogCapture
}

// seedLiveReconnect wires a Processor whose stream has a LIVE source (stamped generation), routing
// input, tenant capacity, a push target, Decklog, and a federation notifier — plus a mock DB that
// reports an ACTIVE ingest session (the reconnect) for the offline fence. The trigger carries no
// event time, so the reaper is a no-op and only the fence runs.
func seedLiveReconnect(t *testing.T, internal, node, tenant string) (*Processor, offlineObservers) {
	t.Helper()
	reg := installRegistryForTest(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })

	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prevDB := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prevDB); dbMock.Close() })
	// The fence probes (under the stream advisory lock) for an active ingest session — the live
	// reconnect — and suppresses; the DVR backstop's generation-fenced claim runs regardless (no rows).
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectCommit()
	mock.ExpectQuery(`UPDATE foghorn.artifacts`).WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))

	// The established live source (a projected DB winner) on the owner node.
	projectSourceForTest(t, reg, internal, node, 100, "uuid-live", "gen-live", 1)
	sm.SetStreamInstanceInputs(internal, node, 1)
	capacity.RegisterStream(tenant, internal)
	streamName := "live+" + internal
	trackPushTargets(streamName, tenant, []*commodorepb.PushTargetInternal{
		{Id: "target-1", TargetUri: "rtmp://example/push"},
	})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	capture, client := startDecklogCapture(t)
	peer := &recordingPeerNotifier{}
	p := minimalProcessorForStreamEnd(t)
	p.decklogClient = client
	p.SetPeerNotifier(peer)
	// Give the resolver the tenant so any tenant-scoped effect that DID run would be attributable
	// (proving suppression, not an accidental unresolved-tenant short-circuit).
	p.streamCache.Set(tenant+":"+internal, streamContext{TenantID: tenant, StreamID: "stream-x"}, time.Minute)

	return p, offlineObservers{reg: reg, sm: sm, capacity: capacity, peer: peer, capture: capture}
}

// assertEverythingStillLive is the shared assertion: a stale STREAM_END/vanish for the OLD
// connection, suppressed by the live reconnect, must have taken NOTHING offline.
func assertEverythingStillLive(t *testing.T, o offlineObservers, internal, node, tenant, wantGeneration string) {
	t.Helper()
	if gen, active, ok := o.reg.SourceGenerationSnapshot(internal, node); !ok || !active || gen != wantGeneration {
		t.Fatalf("registry source must stay active on its generation (ok=%v active=%v gen=%q want=%q)", ok, active, gen, wantGeneration)
	}
	if inst, present := o.sm.GetStreamInstances(internal)[node]; !present || inst.Inputs != 1 {
		t.Fatalf("per-node routing input must be intact, present=%v %+v", present, inst)
	}
	if !o.capacity.HasStream(tenant, internal) {
		t.Fatal("tenant capacity must NOT be decremented for a live reconnect")
	}
	if _, found := lookupPushTarget("live+"+internal, "rtmp://example/push"); !found {
		t.Fatal("push-target tracking must NOT be dropped for a live reconnect")
	}
	if offline, untrack := o.peer.counts(); offline != 0 || untrack != 0 {
		t.Fatalf("federation must stay live: offline-broadcasts=%d untracks=%d, want 0/0", offline, untrack)
	}
	if seen := streamEndTriggersSeen(o.capture.received()); seen != 0 {
		t.Fatalf("no STREAM_END must be forwarded to Decklog for a live reconnect, forwarded %d", seen)
	}
}

// A stale/late STREAM_END for the OLD connection, while a live reconnect holds an active ingest
// session, must take NOTHING offline — registry, per-node input, Decklog, tenant capacity,
// push-target tracking, and federation all stay live for a live reconnect — every stream-wide
// offline effect, not just the registry flip and per-node input.
func TestHandleStreamEnd_LiveReconnectSuppressesAllOfflineEffects(t *testing.T) {
	const internal, node, tenant = "reconnect-live-se", "node-A", "tenant-x"
	p, o := seedLiveReconnect(t, internal, node, tenant)
	tenantStr := tenant
	admitGen, _, _ := o.reg.SourceGenerationSnapshot(internal, node)

	if _, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId:         node,
		TenantId:       &tenantStr,
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal}},
	}); err != nil {
		t.Fatalf("handleStreamEnd: %v", err)
	}
	assertEverythingStillLive(t, o, internal, node, tenant, admitGen)
}

// The vanish (STREAM_LIFECYCLE_UPDATE offline) path must suppress the SAME set of offline effects
// for a live reconnect — the equivalent the earlier test set was missing.
func TestOwnerVanish_LiveReconnectSuppressesAllOfflineEffects(t *testing.T) {
	const internal, node, tenant = "reconnect-live-vanish", "node-A", "tenant-x"
	p, o := seedLiveReconnect(t, internal, node, tenant)
	tenantStr := tenant
	admitGen, _, _ := o.reg.SourceGenerationSnapshot(internal, node)

	if _, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		NodeId:      node,
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId:     &tenantStr,
				NodeId:       node,
				InternalName: internal,
				Status:       "offline",
			},
		},
	}); err != nil {
		t.Fatalf("handleStreamLifecycleUpdate: %v", err)
	}
	assertEverythingStillLive(t, o, internal, node, tenant, admitGen)
}
