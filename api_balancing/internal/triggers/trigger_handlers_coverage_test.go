package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// minimalProcessorTrigHandlers builds a Processor wired only with the pieces
// the per-trigger decision arms need: logger + streamCache (via NewProcessor).
// decklogClient/loadBalancer/geoip/commodore are all nil; every handler is
// nil-tolerant on those (decklog send fails closed, geoip is nil-checked,
// commodore is bypassed by cache-seeded streamContext). peerNotifier is nil
// and every reference is guarded.
func minimalProcessorTrigHandlers(t *testing.T) *Processor {
	t.Helper()
	return NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
}

// installRegistryTrigHandlers swaps a fresh StreamRegistry into the package
// global and restores the previous one on cleanup. handlePushInputClose reads
// control.StreamRegistryInstance directly.
func installRegistryTrigHandlers(t *testing.T) *control.StreamRegistry {
	t.Helper()
	prev := control.StreamRegistryInstance
	reg := control.NewStreamRegistry(nil, "cluster-A", time.Minute)
	control.StreamRegistryInstance = reg
	t.Cleanup(func() { control.StreamRegistryInstance = prev })
	return reg
}

// resetStateTrigHandlers gives each test a fresh stream-state + tenant-capacity
// manager so viewer/stream registrations don't leak across cases.
func resetStateTrigHandlers(t *testing.T) *state.StreamStateManager {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() {
		state.ResetDefaultManagerForTests()
		state.ResetDefaultTenantCapacityForTests()
		sm.Shutdown()
	})
	return sm
}

// ptrTrigHandlers returns a pointer to a string literal for proto optional
// fields. Suffix keeps it from colliding with sibling test helpers.
func ptrTrigHandlers(s string) *string { return &s }

// TestHandlePushInputClose_FlipsSourceInactiveForResume locks the canonical
// "source_active=false" edge: PUSH_INPUT_CLOSE (publisher source
// disconnected) must flip the registry's source-presence state so the next
// PUSH_REWRITE for the same stream is admitted via the resume path instead of
// rejected as a duplicate. This is the primary edge AdmitAndReserve relies on
// (STREAM_END is only the belt-and-suspenders backstop).
func TestHandlePushInputClose_FlipsSourceInactiveForResume(t *testing.T) {
	reg := installRegistryTrigHandlers(t)
	resetStateTrigHandlers(t)

	const internal = "pic-stream-1"
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}

	p := minimalProcessorTrigHandlers(t)
	_, abort, err := p.handlePushInputClose(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_PushInputClose{
			PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handlePushInputClose err: %v", err)
	}
	if abort {
		t.Fatalf("PUSH_INPUT_CLOSE is async/non-blocking, must not abort")
	}

	// Source-presence must be cleared so node-A can resume.
	r := reg.AdmitAndReserve(internal, "node-A", nil)
	if r.Decision != control.AdmissionAcceptResume {
		t.Errorf("post-PUSH_INPUT_CLOSE admit = %v, want AdmissionAcceptResume", r.Decision)
	}
}

// TestHandlePushInputClose_ProcessingPrefixSkipsRegistryFlip locks the
// invariant that a processing+ PUSH_INPUT_CLOSE (a transcoding job's input
// process exiting — sidecar-local job lifecycle, NOT publisher admission)
// must NOT touch publisher source-presence state. Flipping it here would let
// a phantom resume/takeover race the live publisher.
func TestHandlePushInputClose_ProcessingPrefixSkipsRegistryFlip(t *testing.T) {
	reg := installRegistryTrigHandlers(t)
	resetStateTrigHandlers(t)

	const internal = "pic-proc-1"
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}

	p := minimalProcessorTrigHandlers(t)
	_, _, err := p.handlePushInputClose(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_PushInputClose{
			PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: "processing+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handlePushInputClose err: %v", err)
	}

	// node-A must still own the live source: a takeover from node-B rejects.
	r := reg.AdmitAndReserve(internal, "node-B", nil)
	if r.Decision != control.AdmissionRejectDuplicate {
		t.Errorf("processing+ PUSH_INPUT_CLOSE must not clear source: admit = %v, want AdmissionRejectDuplicate", r.Decision)
	}
}

// TestHandlePushEnd_ProcessingPrefixSidecarHandled locks the invariant that a
// processing+ PUSH_END is handled sidecar-side (the job handler signals it),
// so Foghorn returns the no-op result without forwarding to Decklog or
// touching push-target status.
func TestHandlePushEnd_ProcessingPrefixSidecarHandled(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)

	resp, abort, err := p.handlePushEnd(&ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_PushEnd{
			PushEnd: &ipcpb.PushEndTrigger{StreamName: "processing+job-1"},
		},
	})
	if err != nil || abort || resp != "" {
		t.Fatalf("processing+ PUSH_END must be a silent no-op: resp=%q abort=%v err=%v", resp, abort, err)
	}
}

// TestHandlePushEnd_DecklogErrorSurfacingContract pins the error-surfacing
// contract of PUSH_END. With a nil decklog client and a tenant_id present the
// Decklog send always fails; whether that failure is surfaced to Mist (aborts
// the trigger) is gated by trigger type. PUSH_END is in the surface set, so a
// send failure MUST propagate when the type is stamped, and MUST be swallowed
// otherwise (so a fire-and-forget webhook can't wedge Mist's control path).
func TestHandlePushEnd_DecklogErrorSurfacingContract(t *testing.T) {
	resetStateTrigHandlers(t)
	tenantID := "tenant-1"

	t.Run("surfaced when trigger type is PUSH_END", func(t *testing.T) {
		p := minimalProcessorTrigHandlers(t)
		_, _, err := p.handlePushEnd(&ipcpb.MistTrigger{
			TriggerType: "PUSH_END",
			TenantId:    &tenantID,
			TriggerPayload: &ipcpb.MistTrigger_PushEnd{
				// No target URI -> no push-target status goroutine spawned.
				PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+pe-surface"},
			},
		})
		if err == nil {
			t.Fatal("PUSH_END decklog failure must surface (abort the trigger)")
		}
	})

	t.Run("swallowed when trigger type is unset", func(t *testing.T) {
		p := minimalProcessorTrigHandlers(t)
		_, _, err := p.handlePushEnd(&ipcpb.MistTrigger{
			TenantId: &tenantID,
			TriggerPayload: &ipcpb.MistTrigger_PushEnd{
				PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+pe-swallow"},
			},
		})
		if err != nil {
			t.Fatalf("non-surface PUSH_END must swallow decklog failure, got %v", err)
		}
	})
}

// TestHandlePushOutStart_ReturnsPushTargetAsRewrite locks the routing decision:
// PUSH_OUT_START is a blocking trigger whose return value is the push target
// URL Mist should push to. The handler must echo the requested target back as
// the rewrite result (Mist uses it verbatim).
func TestHandlePushOutStart_ReturnsPushTargetAsRewrite(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)

	const target = "rtmp://relay.example/app/key"
	resp, abort, err := p.handlePushOutStart(&ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_PushOutStart{
			PushOutStart: &ipcpb.PushOutStartTrigger{
				StreamName: "live+pos-stream",
				PushTarget: target,
			},
		},
	})
	if err != nil {
		t.Fatalf("handlePushOutStart err: %v", err)
	}
	if abort {
		t.Fatalf("PUSH_OUT_START blocking rewrite must not abort")
	}
	if resp != target {
		t.Fatalf("PUSH_OUT_START must echo the push target as the rewrite: got %q want %q", resp, target)
	}
}

// TestHandleStreamBuffer_PersistsLiveStateForInternalName locks that
// STREAM_BUFFER updates stream state keyed by the EXTRACTED internal name
// (not the wildcard stream name), marking it live with the reported buffer
// state. Keying by internal name avoids duplicate state entries for the same
// logical stream; the balancer reads this live state.
func TestHandleStreamBuffer_PersistsLiveStateForInternalName(t *testing.T) {
	sm := resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)

	const internal = "buf-stream"
	const nodeID = "node-buf"
	_, abort, err := p.handleStreamBuffer(&ipcpb.MistTrigger{
		NodeId: nodeID,
		TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{
			StreamBuffer: &ipcpb.StreamBufferTrigger{
				StreamName:  "live+" + internal,
				BufferState: "FULL",
			},
		},
	})
	if err != nil || abort {
		t.Fatalf("handleStreamBuffer err=%v abort=%v", err, abort)
	}

	got := sm.GetStreamState(internal)
	if got == nil {
		t.Fatalf("STREAM_BUFFER must create live state for internal name %q", internal)
	}
	if got.Status != "live" {
		t.Errorf("buffer event must mark stream live, got status %q", got.Status)
	}
	if got.BufferState != "FULL" {
		t.Errorf("buffer state not recorded: got %q want FULL", got.BufferState)
	}
	if got.NodeID != nodeID {
		t.Errorf("state node mismatch: got %q want %q", got.NodeID, nodeID)
	}
	// Wildcard name must NOT create a separate entry.
	if dup := sm.GetStreamState("live+" + internal); dup != nil {
		t.Errorf("STREAM_BUFFER created a duplicate state entry under the wildcard name")
	}
}

// TestHandleLiveTrackList_PersistsTenantScopedTrackState locks the tenant
// isolation invariant for LIVE_TRACK_LIST: track-list state is keyed by
// internal name and stamped with the tenant resolved from streamContext.
// A cache-seeded streamContext supplies the tenant without a Commodore call;
// the handler must propagate that tenant onto the persisted state so the
// rollup can't be mis-attributed.
func TestHandleLiveTrackList_PersistsTenantScopedTrackState(t *testing.T) {
	sm := resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)

	const internal = "tl-stream"
	const tenantID = "tenant-tl"
	const nodeID = "node-tl"
	// Seed streamContext so applyStreamContext returns the tenant from cache
	// (commodore is nil; the cache hit is the only source of tenant identity).
	p.streamCache.Set(tenantID+":"+internal, streamContext{TenantID: tenantID}, time.Minute)

	_, abort, err := p.handleLiveTrackList(&ipcpb.MistTrigger{
		NodeId:   nodeID,
		TenantId: ptrTrigHandlers(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_TrackList{
			TrackList: &ipcpb.StreamTrackListTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil || abort {
		t.Fatalf("handleLiveTrackList err=%v abort=%v", err, abort)
	}

	got := sm.GetStreamState(internal)
	if got == nil {
		t.Fatalf("LIVE_TRACK_LIST must persist track state for %q", internal)
	}
	if got.TenantID != tenantID {
		t.Errorf("track-list state must carry resolved tenant: got %q want %q", got.TenantID, tenantID)
	}
}

// TestHandleRecordingEnd_SurfacesDecklogFailure locks the RECORDING_END
// error-surfacing contract: it is in the surface set, so when the Decklog
// send fails (nil client, tenant present) the failure must abort the trigger.
// Recording completion drives billing/artifact finalization; a lost event is
// not safe to swallow silently.
func TestHandleRecordingEnd_SurfacesDecklogFailure(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)
	tenantID := "tenant-rec"

	_, _, err := p.handleRecordingEnd(&ipcpb.MistTrigger{
		TriggerType: "RECORDING_END",
		TenantId:    &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_RecordingComplete{
			RecordingComplete: &ipcpb.RecordingCompleteTrigger{
				StreamName: "live+rec-stream",
				FilePath:   "/recordings/rec-stream.mp4",
			},
		},
	})
	if err == nil {
		t.Fatal("RECORDING_END decklog failure must surface (abort the trigger)")
	}
}

// TestHandleRecordingSegment_SurfacesDecklogFailure locks the same surfacing
// contract for RECORDING_SEGMENT: it is in the surface set (per-segment data
// feeds analytics/billing), so a Decklog send failure must abort.
func TestHandleRecordingSegment_SurfacesDecklogFailure(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)
	tenantID := "tenant-seg"

	_, _, err := p.handleRecordingSegment(&ipcpb.MistTrigger{
		TriggerType: "RECORDING_SEGMENT",
		TenantId:    &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_RecordingSegment{
			RecordingSegment: &ipcpb.RecordingSegmentTrigger{
				StreamName: "live+seg-stream",
				FilePath:   "/recordings/seg-stream-0001.ts",
				DurationMs: 6000,
			},
		},
	})
	if err == nil {
		t.Fatal("RECORDING_SEGMENT decklog failure must surface (abort the trigger)")
	}
}

// TestHandleUserNew_NonViewerConnectorAdmittedWithoutPolicy locks the early
// short-circuit: a USER_NEW from a non-playback connector (e.g. an info_json
// metadata fetch) is not a viewer, so it must be admitted ("true") without
// running the viewer load gate, tenant cap, or playback-policy check.
func TestHandleUserNew_NonViewerConnectorAdmittedWithoutPolicy(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)
	tenantID := "tenant-un"

	resp, abort, err := p.handleUserNew(&ipcpb.MistTrigger{
		NodeId:   "node-un",
		TenantId: &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
			ViewerConnect: &ipcpb.ViewerConnectTrigger{
				StreamName: "live+un-stream",
				// A metadata/info request, not a playback viewer.
				Connector:  "HTTP",
				RequestUrl: "https://edge.example/json_un-stream.js",
				SessionId:  "sess-meta",
			},
		},
	})
	if err != nil || abort {
		t.Fatalf("handleUserNew err=%v abort=%v", err, abort)
	}
	if resp != "true" {
		t.Fatalf("non-viewer USER_NEW must be admitted: got %q want true", resp)
	}
}

// TestHandleUserNew_TenantViewerCapRejects locks the per-tenant concurrent
// viewer cap: a hard limit independent of cluster load. With the tenant
// already at its max_viewers (cached in streamContext at PUSH_REWRITE) a new
// distinct viewer must be rejected ("false"). Cap value flows from the seeded
// streamContext, so no Commodore round-trip is needed.
func TestHandleUserNew_TenantViewerCapRejects(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)

	const internal = "cap-stream"
	const tenantID = "tenant-cap"
	// RequiresAuthKnown+!RequiresAuth would otherwise admit; MaxViewers=1 gates
	// before policy. Seed the cap and an authoritative tenant.
	p.streamCache.Set(tenantID+":"+internal, streamContext{
		TenantID:          tenantID,
		MaxViewers:        1,
		RequiresAuthKnown: true,
		RequiresAuth:      false,
	}, time.Minute)

	// Fill the tenant's single viewer slot with a DIFFERENT capacity id.
	state.DefaultTenantCapacity().RegisterViewer(tenantID, "existing-viewer")

	resp, abort, err := p.handleUserNew(&ipcpb.MistTrigger{
		NodeId:   "node-cap",
		TenantId: ptrTrigHandlers(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
			ViewerConnect: &ipcpb.ViewerConnectTrigger{
				StreamName: "live+" + internal,
				Connector:  "HLS",
				RequestUrl: "https://edge.example/hls/" + internal + "/index.m3u8",
				SessionId:  "sess-new",
			},
		},
	})
	if err != nil || abort {
		t.Fatalf("handleUserNew err=%v abort=%v", err, abort)
	}
	if resp != "false" {
		t.Fatalf("viewer over tenant cap must be rejected: got %q want false", resp)
	}
}

// TestHandleUserNew_AdmittedViewerRegistersUnderCap locks the admit side of
// the same gate: a viewer within the tenant cap on an unauthenticated stream
// is admitted ("true") AND counted, so the next viewer sees an incremented
// occupancy. This proves the cap is enforced from real registered occupancy,
// not a static check.
func TestHandleUserNew_AdmittedViewerRegistersUnderCap(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)

	const internal = "admit-stream"
	const tenantID = "tenant-admit"
	p.streamCache.Set(tenantID+":"+internal, streamContext{
		TenantID:          tenantID,
		MaxViewers:        5,
		RequiresAuthKnown: true,
		RequiresAuth:      false,
	}, time.Minute)

	before := state.DefaultTenantCapacity().CountViewers(tenantID)
	resp, abort, err := p.handleUserNew(&ipcpb.MistTrigger{
		NodeId:   "node-admit",
		TenantId: ptrTrigHandlers(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{
			ViewerConnect: &ipcpb.ViewerConnectTrigger{
				StreamName: "live+" + internal,
				Connector:  "HLS",
				RequestUrl: "https://edge.example/hls/" + internal + "/index.m3u8",
				SessionId:  "sess-admit",
			},
		},
	})
	if err != nil || abort {
		t.Fatalf("handleUserNew err=%v abort=%v", err, abort)
	}
	if resp != "true" {
		t.Fatalf("within-cap unauthenticated viewer must be admitted: got %q want true", resp)
	}
	if after := state.DefaultTenantCapacity().CountViewers(tenantID); after != before+1 {
		t.Errorf("admitted viewer must be registered against the cap: count %d -> %d", before, after)
	}
}
