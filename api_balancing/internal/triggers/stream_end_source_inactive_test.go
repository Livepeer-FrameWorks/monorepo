package triggers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestHandleStreamEnd_GenuinelyOfflineStillFinalizes is the complement: with NO active ingest
// session (the publisher truly stopped and the close already flipped the registry source), the
// backstop must still run — a clean end must flip the source inactive so the next PUSH_REWRITE is
// admitted. Proves the fence does not over-suppress the normal offline path.
func TestHandleStreamEnd_GenuinelyOfflineStillFinalizes(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prevDB := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prevDB); dbMock.Close() })
	// The genuinely-offline path: the fence takes the stream advisory lock, probes (no active session),
	// flips, and commits; then the authoritative re-probe (standalone) immediately before the
	// destructive effects also reports no active session; then the DVR backstop claims nothing.
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`nextval`).WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(2)))
	expectOfflineEffectInsert(mock)
	mock.ExpectCommit()
	mock.ExpectQuery(`UPDATE foghorn.artifacts`).WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))

	const internal = "offline-final-1"
	tenant := "tenant-x"
	projectSourceForTest(t, reg, internal, "node-A", 100, "uuid-gone", "gen-gone", 1)

	p := minimalProcessorForStreamEnd(t)
	if _, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId:         "node-A",
		TenantId:       &tenant,
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal}},
	}); err != nil {
		t.Fatalf("handleStreamEnd: %v", err)
	}

	// Genuinely offline: the source must be flipped inactive (a later push resumes).
	if _, active, ok := reg.SourceGenerationSnapshot(internal, "node-A"); !ok || active {
		t.Fatalf("a genuinely-offline STREAM_END must flip the source inactive (active=%v ok=%v)", active, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// installRegistryForTest swaps a fresh StreamRegistry into the package
// global and restores the previous one on cleanup. Required because
// handleStreamEnd reads control.StreamRegistryInstance directly.
func installRegistryForTest(t *testing.T) *control.StreamRegistry {
	t.Helper()
	prev := control.StreamRegistryInstance
	reg := control.NewStreamRegistry(nil, "cluster-A", time.Minute)
	control.StreamRegistryInstance = reg
	t.Cleanup(func() { control.StreamRegistryInstance = prev })
	return reg
}

// minimalProcessorForStreamEnd builds a Processor wired only with the
// pieces handleStreamEnd needs: logger, streamCache (via NewProcessor),
// no decklog (nil-tolerant), no peerNotifier (nil-checked), no
// quartermaster (cluster-owner cache nil-safe).
func minimalProcessorForStreamEnd(t *testing.T) *Processor {
	t.Helper()
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.sendDeactivatePushTargets = func(context.Context, string, *ipcpb.DeactivatePushTargets) error { return nil }
	return p
}

func TestApplyOfflineEffect_RetriesFailedNodeDispatchAfterLocalCleanup(t *testing.T) {
	reg := installRegistryForTest(t)
	const (
		tenant   = "tenant-offline-retry"
		internal = "offline-retry"
		node     = "node-offline-retry"
	)
	projectSourceForTest(t, reg, internal, node, 10, "trigger-retry", "generation-retry", 1)

	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	capacity.RegisterStream(tenant, internal)
	trackPushTargets("live+"+internal, tenant, []*commodorepb.PushTargetInternal{{TargetUri: "rtmp://example/retry"}})
	t.Cleanup(func() { untrackPushTargets("live+" + internal) })

	dispatchErr := errors.New("control stream unavailable")
	p := minimalProcessorForStreamEnd(t)
	p.sendDeactivatePushTargets = func(context.Context, string, *ipcpb.DeactivatePushTargets) error { return dispatchErr }
	err := p.ApplyOfflineEffect(context.Background(), control.OfflineEffect{
		TenantID: tenant, InternalName: internal, NodeID: node,
		SourceGeneration: "generation-retry", SourceRevision: 2,
		SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
	})
	if !errors.Is(err, dispatchErr) {
		t.Fatalf("ApplyOfflineEffect error = %v, want node dispatch failure", err)
	}
	if capacity.HasStream(tenant, internal) {
		t.Fatal("local capacity cleanup must not wait for the node dispatch retry")
	}
	if _, found := lookupPushTarget("live+"+internal, "rtmp://example/retry"); found {
		t.Fatal("local push-target tracking must be cleared before the node dispatch retry")
	}
}

func TestApplyOfflineEffect_ThreadsCanceledContextToNodeDispatch(t *testing.T) {
	reg := installRegistryForTest(t)
	const internal = "offline-context-fence"
	projectSourceForTest(t, reg, internal, "node-context", 11, "trigger-context", "generation-context", 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var observed error
	p := minimalProcessorForStreamEnd(t)
	p.sendDeactivatePushTargets = func(dispatchCtx context.Context, _ string, _ *ipcpb.DeactivatePushTargets) error {
		observed = dispatchCtx.Err()
		return observed
	}
	err := p.ApplyOfflineEffect(ctx, control.OfflineEffect{
		TenantID:         "tenant-context",
		InternalName:     internal,
		NodeID:           "node-context",
		SourceGeneration: "generation-context",
		SourceRevision:   2,
		TeardownStream:   true,
	})
	if !errors.Is(observed, context.Canceled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("effect context was not threaded to dispatch: observed=%v err=%v", observed, err)
	}
}

func installOfflineFenceDB(t *testing.T, includeDVRBackstop bool) sqlmock.Sqlmock {
	t.Helper()
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	prevDB := control.GetDB()
	control.SetDB(dbMock)
	t.Cleanup(func() { control.SetDB(prevDB); _ = dbMock.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT EXISTS`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`nextval`).WillReturnRows(sqlmock.NewRows([]string{"nextval"}).AddRow(int64(2)))
	expectOfflineEffectInsert(mock)
	mock.ExpectCommit()
	if includeDVRBackstop {
		mock.ExpectQuery(`UPDATE foghorn.artifacts`).WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "node_id"}))
	}
	return mock
}

// seedActiveSource projects a DB-confirmed source generation.
func seedActiveSource(t *testing.T, reg *control.StreamRegistry, internal, node string) {
	t.Helper()
	projectSourceForTest(t, reg, internal, node, 0, "", "gen-"+node, 1)
}

// TestHandleStreamEnd_FlipsSourceInactiveOnMatchingNode verifies the aggregate backstop when the
// publisher's precise close is unavailable. The durable offline obligation must make the ended
// source inactive so a later publisher can be admitted.
func TestHandleStreamEnd_FlipsSourceInactiveOnMatchingNode(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const internal = "stream-end-1"
	seedActiveSource(t, reg, internal, "node-A")
	mock := installOfflineFenceDB(t, true)
	tenant := "tenant-stream-end"

	p := minimalProcessorForStreamEnd(t)
	_, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId:   "node-A",
		TenantId: &tenant,
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
			StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamEnd returned err: %v", err)
	}

	// SourceActive must be false now (owner retained), so a subsequent PUSH_REWRITE from node-A resumes.
	if _, active, ok := reg.SourceGenerationSnapshot(internal, "node-A"); !ok || active {
		t.Errorf("post-STREAM_END the source must be inactive on node-A (active=%v ok=%v)", active, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestHandleStreamEnd_StaleNodeDoesNotClearLiveOwner guards the
// node-match invariant: a STREAM_END originating from a node that's
// not the recorded owner must not clear that owner's live state.
// Without the guard, a stale/misrouted STREAM_END could let a second
// publisher steal admission via AcceptResume.
func TestHandleStreamEnd_StaleNodeDoesNotClearLiveOwner(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const internal = "stream-end-2"
	projectSourceForTest(t, reg, internal, "node-A", 0, "", "gen-seed", 1)

	p := minimalProcessorForStreamEnd(t)
	// STREAM_END from a DIFFERENT node than the recorded owner.
	_, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId: "node-B",
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
			StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamEnd returned err: %v", err)
	}

	// Node-A is still the live owner — the stale STREAM_END must not have cleared its source.
	// (Rejecting a duplicate is now the DB's job, not the registry's; the registry-level invariant
	// is simply that the live source survives.)
	if _, active, ok := reg.SourceGenerationSnapshot(internal, "node-A"); !ok || !active {
		t.Errorf("live owner node-A must be preserved after a stale STREAM_END (active=%v ok=%v)", active, ok)
	}
}

// TestOfflineIsStreamWide_OwnerTyped locks the offline authority rule:
// authority is stamped at source start and is the ONLY thing consumed at
// source end. The recorded owner ending its stream is stream-wide even
// while a replica drains inputs; everything without a recorded owner —
// non-owners, replicas (which never get one), sole local carriers,
// missing registry — is node-local. Absence is not authority.
func TestOfflineIsStreamWide_OwnerTyped(t *testing.T) {
	reg := installRegistryForTest(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const owned = "owner-typed-1"
	projectSourceForTest(t, reg, owned, "node-ingest", 0, "", "gen-seed", 1)
	// A replica edge still actively carries the stream.
	sm.UpdateNodeStats(owned, "node-replica", 3, 1, 100, 200, true)

	p := minimalProcessorForStreamEnd(t)
	if !p.offlineIsStreamWide(owned, "node-ingest") {
		t.Fatal("owner ending must be stream-wide even with a replica draining inputs")
	}
	if p.offlineIsStreamWide(owned, "node-replica") {
		t.Fatal("replica ending must stay node-local while an owner is recorded")
	}

	// An inactive projection retains ownership, so the delayed aggregate edge remains stream-wide.
	markSourceInactiveForTest(t, reg, owned, "node-ingest", "", 2)
	if !p.offlineIsStreamWide(owned, "node-ingest") {
		t.Fatal("owner ending must stay stream-wide after the inactive projection")
	}

	// No recorded owner: node-local, even for the sole local carrier.
	// A cross-cluster replica is exactly this shape — treating "last
	// local input" as authority would let it flip a stream that is
	// live in its origin cluster.
	const ownerless = "ownerless-1"
	sm.UpdateNodeStats(ownerless, "node-B", 3, 1, 100, 200, false)
	if p.offlineIsStreamWide(ownerless, "node-A") {
		t.Fatal("ownerless ending must stay node-local while another node carries the stream")
	}
	if p.offlineIsStreamWide(ownerless, "node-B") {
		t.Fatal("ownerless ending of the sole carrier must stay node-local (absence is not authority)")
	}
}

// TestOfflineIsStreamWide_ReplicaNeverStreamWide seeds a stream exactly the
// way cross-cluster replication does (MarkReplicating on the dest cluster,
// which never records an owner) and proves the replica's ending is
// node-local both while the transient replication mark is set AND after
// checkReplicationCompletion has cleared it — the production ordering.
func TestOfflineIsStreamWide_ReplicaNeverStreamWide(t *testing.T) {
	reg := installRegistryForTest(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const internal = "replicated-1"
	// MarkReplicating creates the minimal entry itself — exactly what the
	// dest cluster has before any resolver populates stream identity.
	reg.MarkReplicating(internal, "peer-cluster-eu", "dtsc://origin-edge:4200", "node-replica", "https://replica.example/view", "origin-node")
	// The replica is the sole local carrier.
	sm.UpdateNodeStats(internal, "node-replica", 3, 1, 100, 200, true)

	p := minimalProcessorForStreamEnd(t)
	if p.offlineIsStreamWide(internal, "node-replica") {
		t.Fatal("replica ending must be node-local while the replication mark is set")
	}

	// checkReplicationCompletion clears ReplicatingFrom the moment the
	// replica goes live — long before it ends. Authority must not change.
	reg.ClearReplicating(internal)
	if p.offlineIsStreamWide(internal, "node-replica") {
		t.Fatal("replica ending must stay node-local after the replication mark is cleared")
	}
}

// TestOwnerVanishRunsStreamEndFinalization proves the owner's vanish (a
// lifecycle offline standing in for a missed/delayed STREAM_END) runs the
// same owner-end cleanup as a real STREAM_END: the tenant's
// concurrent-stream count drops, push-target tracking clears, and
// SourceActive flips so the publisher's reconnect takes the resume path
// instead of being rejected as a duplicate.
// When the stream owner is UNRESOLVABLE and the node merely ASSERTS a tenant, the owner-vanish path must
// retain every stream-wide effect when ownership cannot be resolved. Running only a subset would still
// mutate a stream whose tenant scope was never authenticated.
func TestOwnerVanishUnresolvedTenantSkipsTenantScopedFinalization(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })

	const internal = "vanish-unresolved-1"
	const assertedTenant = "tenant-forged"
	streamName := "live+" + internal
	seedActiveSource(t, reg, internal, "node-ingest")
	// The stream's capacity is held under the forged tenant to prove it is NOT decremented by an unverified path.
	capacity.RegisterStream(assertedTenant, internal)
	trackPushTargets(streamName, assertedTenant, []*commodorepb.PushTargetInternal{{Id: "t1", TargetUri: "rtmp://example/push"}})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	tenant := assertedTenant
	streamID := "b3b1c1de-0000-4000-8000-000000000003"
	p := minimalProcessorForStreamEnd(t) // NO resolver seed → owner is unresolvable
	if _, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		StreamId:    &streamID,
		NodeId:      "node-ingest",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId: &tenant, NodeId: "node-ingest", InternalName: internal, Status: "offline",
			},
		},
	}); err != nil {
		t.Fatalf("owner vanish: %v", err)
	}

	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); !found {
		t.Fatal("an unresolved vanish must retain push-target tracking")
	}
	// Tenant-scoped effect must NOT run under the unverified asserted tenant: the forged tenant's count stands.
	if !capacity.HasStream(assertedTenant, internal) {
		t.Fatal("an unresolved node-asserted tenant must NOT drive the concurrent-stream decrement")
	}
}

func TestOwnerVanishRunsStreamEndFinalization(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })

	const internal = "vanish-finalize-1"
	const tenantID = "tenant-vanish"
	streamName := "live+" + internal
	seedActiveSource(t, reg, internal, "node-ingest")
	capacity.RegisterStream(tenantID, internal)
	trackPushTargets(streamName, tenantID, []*commodorepb.PushTargetInternal{
		{Id: "target-1", TargetUri: "rtmp://example/push"},
	})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	tenant := tenantID
	streamID := "b3b1c1de-0000-4000-8000-000000000002"
	p := minimalProcessorForStreamEnd(t)
	mock := installOfflineFenceDB(t, true)
	// The stream was admitted under a resolvable owner; seed the resolver so owner-vanish finalization runs with
	// the AUTHORITATIVE tenant. (An UNRESOLVED node-asserted tenant is deliberately NOT trusted for the
	// tenant-scoped effects — see TestOwnerVanishUnresolvedTenantSkipsTenantScopedFinalization.)
	p.streamCache.Set(tenantID+":"+internal, streamContext{TenantID: tenantID, StreamID: streamID}, time.Minute)
	_, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		StreamId:    &streamID,
		NodeId:      "node-ingest",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId:     &tenant,
				NodeId:       "node-ingest",
				InternalName: internal,
				Status:       "offline",
			},
		},
	})
	if err != nil {
		t.Fatalf("owner vanish handleStreamLifecycleUpdate: %v", err)
	}
	if err := p.ApplyOfflineEffect(context.Background(), control.OfflineEffect{
		TenantID: tenantID, InternalName: internal, NodeID: "node-ingest", SourceRevision: 2,
		SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
	}); err != nil {
		t.Fatalf("apply durable offline effect: %v", err)
	}

	if capacity.HasStream(tenantID, internal) {
		t.Fatal("owner vanish must decrement the tenant's concurrent-stream count")
	}
	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); found {
		t.Fatal("owner vanish must drop push-target tracking")
	}
	// SourceActive cleared with ownership retained → a same-node reconnect resumes.
	if _, active, ok := reg.SourceGenerationSnapshot(internal, "node-ingest"); !ok || active {
		t.Fatalf("post-vanish the source must be inactive on node-ingest (active=%v ok=%v)", active, ok)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestReplicaVanishSkipsStreamEndFinalization is the counterpart: a
// non-owner's vanish is a node-local fact and must leave every stream-wide
// state untouched — capacity, push-target tracking, and the owner's live
// admission claim.
func TestReplicaVanishSkipsStreamEndFinalization(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })

	const internal = "vanish-replica-1"
	const tenantID = "tenant-vanish-replica"
	streamName := "live+" + internal
	seedActiveSource(t, reg, internal, "node-ingest")
	capacity.RegisterStream(tenantID, internal)
	trackPushTargets(streamName, tenantID, []*commodorepb.PushTargetInternal{
		{Id: "target-1", TargetUri: "rtmp://example/push"},
	})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	tenant := tenantID
	streamID := "b3b1c1de-0000-4000-8000-000000000003"
	p := minimalProcessorForStreamEnd(t)
	_, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		StreamId:    &streamID,
		NodeId:      "node-replica",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{
			StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId:     &tenant,
				NodeId:       "node-replica",
				InternalName: internal,
				Status:       "offline",
			},
		},
	})
	if err != nil {
		t.Fatalf("replica vanish handleStreamLifecycleUpdate: %v", err)
	}

	if !capacity.HasStream(tenantID, internal) {
		t.Fatal("replica vanish must not decrement the tenant's concurrent-stream count")
	}
	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); !found {
		t.Fatal("replica vanish must not drop push-target tracking")
	}
	// The live owner's source must survive a replica's vanish (the DB, not the registry, rejects a
	// later duplicate — the registry-level invariant is that the live source is not cleared).
	if _, active, ok := reg.SourceGenerationSnapshot(internal, ""); !ok || !active {
		t.Fatalf("the live owner's source must survive a replica vanish (active=%v ok=%v)", active, ok)
	}
}

// TestOfflineIsStreamWide_NilRegistry: with no registry there is no
// authority source at all, so nothing may fast-offline.
func TestOfflineIsStreamWide_NilRegistry(t *testing.T) {
	prev := control.StreamRegistryInstance
	control.StreamRegistryInstance = nil
	t.Cleanup(func() { control.StreamRegistryInstance = prev })
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.UpdateNodeStats("no-registry", "node-A", 1, 1, 0, 0, false)

	p := minimalProcessorForStreamEnd(t)
	if p.offlineIsStreamWide("no-registry", "node-A") {
		t.Fatal("nil registry must never produce a stream-wide offline")
	}
}

// TestHandleStreamEnd_StreamWideEffectsAreOwnerGated locks the side-effect
// split: a replica/non-owner STREAM_END must leave stream-wide state — the
// tenant's concurrent-stream count and the process-global push-target
// tracking (whose loss would silently no-op later PUSH_OUT_START/PUSH_END
// status updates for the still-live owner) — untouched, while the owner's
// STREAM_END ends the stream for real.
func TestHandleStreamEnd_StreamWideEffectsAreOwnerGated(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })

	const internal = "owner-gated-1"
	const tenantID = "tenant-owner-gated"
	streamName := "live+" + internal
	seedActiveSource(t, reg, internal, "node-ingest")
	capacity.RegisterStream(tenantID, internal)
	trackPushTargets(streamName, tenantID, []*commodorepb.PushTargetInternal{
		{Id: "target-1", TargetUri: "rtmp://example/push"},
	})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	p := minimalProcessorForStreamEnd(t)
	endTrigger := func(nodeID string) *ipcpb.MistTrigger {
		tenant := tenantID
		return &ipcpb.MistTrigger{
			NodeId:   nodeID,
			TenantId: &tenant,
			TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
				StreamEnd: &ipcpb.StreamEndTrigger{StreamName: streamName},
			},
		}
	}

	// Replica end: node-local only.
	if _, _, err := p.handleStreamEnd(endTrigger("node-replica")); err != nil {
		t.Fatalf("replica handleStreamEnd: %v", err)
	}
	if !capacity.HasStream(tenantID, internal) {
		t.Fatal("replica STREAM_END must not decrement the tenant's concurrent-stream count")
	}
	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); !found {
		t.Fatal("replica STREAM_END must not drop the owner's push-target tracking")
	}

	// Owner end: the stream itself ends.
	mock := installOfflineFenceDB(t, true)
	if _, _, err := p.handleStreamEnd(endTrigger("node-ingest")); err != nil {
		t.Fatalf("owner handleStreamEnd: %v", err)
	}
	if err := p.ApplyOfflineEffect(context.Background(), control.OfflineEffect{
		TenantID: tenantID, InternalName: internal, NodeID: "node-ingest", SourceRevision: 2,
		SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
	}); err != nil {
		t.Fatalf("apply durable offline effect: %v", err)
	}
	if capacity.HasStream(tenantID, internal) {
		t.Fatal("owner STREAM_END must decrement the tenant's concurrent-stream count")
	}
	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); found {
		t.Fatal("owner STREAM_END must drop push-target tracking")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// TestSourceOwner covers the owner retained by inactive source projections.
func TestSourceOwner(t *testing.T) {
	reg := installRegistryForTest(t)

	if _, known := reg.SourceOwner("unknown-stream"); known {
		t.Fatal("expected no owner for unknown stream")
	}

	const internal = "source-owner-1"
	projectSourceForTest(t, reg, internal, "node-A", 0, "", "gen-seed", 1)
	owner, known := reg.SourceOwner(internal)
	if !known || owner != "node-A" {
		t.Fatalf("SourceOwner = (%q, %v), want (node-A, true)", owner, known)
	}
	// live+ prefixed lookups resolve to the same entry.
	owner, known = reg.SourceOwner("live+" + internal)
	if !known || owner != "node-A" {
		t.Fatalf("SourceOwner(live+) = (%q, %v), want (node-A, true)", owner, known)
	}

	markSourceInactiveForTest(t, reg, internal, "node-A", "", 2)
	owner, known = reg.SourceOwner(internal)
	if !known || owner != "node-A" {
		t.Fatalf("SourceOwner after inactive = (%q, %v), want retained (node-A, true)", owner, known)
	}
}
