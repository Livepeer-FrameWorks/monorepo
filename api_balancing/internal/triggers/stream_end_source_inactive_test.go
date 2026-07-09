package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

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
	return NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
}

// TestHandleStreamEnd_FlipsSourceInactiveOnMatchingNode locks the
// belt-and-suspenders behavior: when PUSH_INPUT_CLOSE is lost
// (Helmsman crash, transport blip, parse error), STREAM_END must
// still flip SourceActive=false on the registry so the next
// PUSH_REWRITE on this stream is admitted via the resume/takeover
// path instead of rejected as a duplicate.
func TestHandleStreamEnd_FlipsSourceInactiveOnMatchingNode(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const internal = "stream-end-1"
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}

	p := minimalProcessorForStreamEnd(t)
	_, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
			StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamEnd returned err: %v", err)
	}

	// SourceActive must be false now, so a subsequent PUSH_REWRITE
	// from node-A succeeds via the resume path.
	r := reg.AdmitAndReserve(internal, "node-A", nil)
	if r.Decision != control.AdmissionAcceptResume {
		t.Errorf("post-STREAM_END admit decision = %v, want AdmissionAcceptResume", r.Decision)
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
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}

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

	// Node-A is still the live owner; a takeover attempt from
	// node-C must still see the live owner and reject.
	r := reg.AdmitAndReserve(internal, "node-C", nil)
	if r.Decision != control.AdmissionRejectDuplicate {
		t.Errorf("post-stale-STREAM_END admit decision = %v, want AdmissionRejectDuplicate (live owner must be preserved)", r.Decision)
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
	if r := reg.AdmitAndReserve(owned, "node-ingest", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}
	// A replica edge still actively carries the stream.
	sm.UpdateNodeStats(owned, "node-replica", 3, 1, 100, 200, true)

	p := minimalProcessorForStreamEnd(t)
	if !p.offlineIsStreamWide(owned, "node-ingest") {
		t.Fatal("owner ending must be stream-wide even with a replica draining inputs")
	}
	if p.offlineIsStreamWide(owned, "node-replica") {
		t.Fatal("replica ending must stay node-local while an owner is recorded")
	}

	// Ownership survives MarkSourceInactive (the PUSH_INPUT_CLOSE →
	// delayed STREAM_END sequence), so the late owner STREAM_END is
	// still typed stream-wide.
	reg.MarkSourceInactive(owned, "node-ingest")
	if !p.offlineIsStreamWide(owned, "node-ingest") {
		t.Fatal("owner ending must stay stream-wide after MarkSourceInactive")
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
func TestOwnerVanishRunsStreamEndFinalization(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })

	const internal = "vanish-finalize-1"
	const tenantID = "tenant-vanish"
	streamName := "live+" + internal
	if r := reg.AdmitAndReserve(internal, "node-ingest", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}
	capacity.RegisterStream(tenantID, internal)
	trackPushTargets(streamName, tenantID, []*commodorepb.PushTargetInternal{
		{Id: "target-1", TargetUri: "rtmp://example/push"},
	})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	tenant := tenantID
	streamID := "b3b1c1de-0000-4000-8000-000000000002"
	p := minimalProcessorForStreamEnd(t)
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

	if capacity.HasStream(tenantID, internal) {
		t.Fatal("owner vanish must decrement the tenant's concurrent-stream count")
	}
	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); found {
		t.Fatal("owner vanish must drop push-target tracking")
	}
	// SourceActive cleared with ownership retained → same-node reconnect
	// takes the resume path, not a duplicate rejection.
	if r := reg.AdmitAndReserve(internal, "node-ingest", nil); r.Decision != control.AdmissionAcceptResume {
		t.Fatalf("post-vanish admit decision = %v, want AdmissionAcceptResume", r.Decision)
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
	if r := reg.AdmitAndReserve(internal, "node-ingest", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}
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
	// The live owner must remain admission-blocking (duplicate rejection).
	if r := reg.AdmitAndReserve(internal, "node-other", nil); r.Decision != control.AdmissionRejectDuplicate {
		t.Fatalf("post-replica-vanish admit decision = %v, want AdmissionRejectDuplicate", r.Decision)
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
	if r := reg.AdmitAndReserve(internal, "node-ingest", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}
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
	if _, _, err := p.handleStreamEnd(endTrigger("node-ingest")); err != nil {
		t.Fatalf("owner handleStreamEnd: %v", err)
	}
	if capacity.HasStream(tenantID, internal) {
		t.Fatal("owner STREAM_END must decrement the tenant's concurrent-stream count")
	}
	if _, found := lookupPushTarget(streamName, "rtmp://example/push"); found {
		t.Fatal("owner STREAM_END must drop push-target tracking")
	}
}

// TestSourceOwner covers the accessor the offline typing relies on:
// unknown streams report no owner, admission records it, and
// MarkSourceInactive retains it for the resume path.
func TestSourceOwner(t *testing.T) {
	reg := installRegistryForTest(t)

	if _, known := reg.SourceOwner("unknown-stream"); known {
		t.Fatal("expected no owner for unknown stream")
	}

	const internal = "source-owner-1"
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}
	owner, known := reg.SourceOwner(internal)
	if !known || owner != "node-A" {
		t.Fatalf("SourceOwner = (%q, %v), want (node-A, true)", owner, known)
	}
	// live+ prefixed lookups resolve to the same entry.
	owner, known = reg.SourceOwner("live+" + internal)
	if !known || owner != "node-A" {
		t.Fatalf("SourceOwner(live+) = (%q, %v), want (node-A, true)", owner, known)
	}

	reg.MarkSourceInactive(internal, "node-A")
	owner, known = reg.SourceOwner(internal)
	if !known || owner != "node-A" {
		t.Fatalf("SourceOwner after inactive = (%q, %v), want retained (node-A, true)", owner, known)
	}
}
