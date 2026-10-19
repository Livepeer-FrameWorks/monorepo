package triggers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// STREAM_END and the poller's vanish each enqueue an offline effect for the
// same ended source. When the vanish takes the later revision, the STREAM_END
// effect loses the revision CAS and none of its state legs may run, but its
// stream_end analytics fact is the only one Periscope gets for the session and
// must still reach Decklog. A superseded vanish forwards only an end inferred for a resolved ingest
// session, and with no session store none resolves.
func TestApplyOfflineEffect_SupersededStreamEndStillForwardsAnalytics(t *testing.T) {
	reg := installRegistryForTest(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client

	const (
		tenant     = "tenant-race"
		internal   = "race-stream"
		node       = "node-race"
		generation = "gen-race"
		streamID   = "11111111-1111-4111-8111-0000000000aa"
	)
	projectSourceForTest(t, reg, internal, node, 7, "trigger-race", generation, 1)
	sm.UpdateNodeStats(internal, node, 1, 1, 0, 0, false)
	// The vanish effect (revision 3) published its inactive projection first.
	if !markSourceInactiveForTest(t, reg, internal, node, generation, 3) {
		t.Fatal("vanish revision was not applied")
	}

	tenantID, sid, reqID := tenant, streamID, "stream-end-request-1"
	streamEnd, err := proto.Marshal(&ipcpb.MistTrigger{
		TriggerType: "STREAM_END", NodeId: node, TenantId: &tenantID, StreamId: &sid, RequestId: reqID,
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal}},
	})
	if err != nil {
		t.Fatalf("marshal STREAM_END: %v", err)
	}
	legs, err := p.ApplyOfflineEffect(context.Background(), control.OfflineEffect{
		TenantID: tenant, InternalName: internal, NodeID: node, SourceGeneration: generation, SourceRevision: 2,
		SetNodeOffline: true, DecklogTrigger: streamEnd,
	})
	if !errors.Is(err, control.ErrOfflineEffectSuperseded) {
		t.Fatalf("ApplyOfflineEffect error = %v, want superseded", err)
	}
	if !legs.DecklogDone || legs.SetNodeOfflineDone {
		t.Fatalf("legs = %+v, want only the Decklog leg done", legs)
	}
	got := capture.received()
	if len(got) != 1 || got[0].GetStreamEnd() == nil || got[0].GetRequestId() != reqID {
		t.Fatalf("Decklog received %d trigger(s) %v, want the superseded STREAM_END once", len(got), got)
	}

	lifecycle, err := proto.Marshal(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: node, TenantId: &tenantID, StreamId: &sid,
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
			TenantId: &tenantID, NodeId: node, InternalName: internal, Status: "offline",
		}},
	})
	if err != nil {
		t.Fatalf("marshal lifecycle: %v", err)
	}
	if _, err := p.ApplyOfflineEffect(context.Background(), control.OfflineEffect{
		TenantID: tenant, InternalName: internal, NodeID: node, SourceGeneration: generation, SourceRevision: 2,
		DecklogTrigger: lifecycle,
	}); !errors.Is(err, control.ErrOfflineEffectSuperseded) {
		t.Fatalf("superseded vanish error = %v, want superseded", err)
	}
	if got := capture.received(); len(got) != 1 {
		t.Fatalf("a superseded vanish was forwarded: Decklog received %d trigger(s)", len(got))
	}
}
