package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// The status and start a pushed stream reports follow its publisher's ingest
// session. After the session ends (stream.idle), Mist still counts the input
// and serves the buffer until its own STREAM_END; those reports must not keep
// the stream live, and the start time is the session's, not the report's.
func TestLifecycleStatusAndStartFollowIngestSession(t *testing.T) {
	const (
		tenantID = "11111111-2222-3333-4444-555555555555"
		streamID = "b3b1c1de-0000-4000-8000-000000000001"
		internal = "session-status"
		nodeID   = "node-A"
	)
	resetStateTrigHandlers(t)
	registry := installRegistryTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)
	p.streamCache.Set(tenantID+":"+internal, streamContext{TenantID: tenantID, StreamID: streamID}, time.Minute)
	report := func() *ipcpb.MistTrigger {
		return &ipcpb.MistTrigger{
			TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: nodeID,
			TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId: proto.String(tenantID), InternalName: "live+" + internal, Status: "live", TotalInputs: proto.Uint32(1),
			}},
		}
	}
	forwarded := func() *ipcpb.StreamLifecycleUpdate {
		t.Helper()
		trigger := report()
		if _, _, err := p.handleStreamLifecycleUpdate(trigger); err != nil {
			t.Fatal(err)
		}
		return trigger.GetStreamLifecycleUpdate()
	}

	sessionStart := time.Now()
	projectSourceForTest(t, registry, internal, nodeID, 41, "push-1", "generation-1", 1)
	time.Sleep(1100 * time.Millisecond)
	live := forwarded()
	if live.GetStatus() != "live" {
		t.Fatalf("status during the session = %q, want live", live.GetStatus())
	}
	if got := live.GetStartedAt(); got < sessionStart.Unix() || got > sessionStart.Unix()+1 {
		t.Fatalf("started_at = %d, want the session start %d", got, sessionStart.Unix())
	}

	markSourceInactiveForTest(t, registry, internal, nodeID, "generation-1", 2)
	ended := forwarded()
	if ended.GetStatus() == "live" {
		t.Fatal("a report after the publisher's session ended kept the stream live")
	}
	if ended.GetStartedAt() != live.GetStartedAt() {
		t.Fatalf("started_at moved after the session ended: %d, was %d", ended.GetStartedAt(), live.GetStartedAt())
	}

	republished := time.Now()
	projectSourceForTest(t, registry, internal, nodeID, 42, "push-2", "generation-2", 3)
	next := forwarded()
	if next.GetStatus() != "live" || next.GetStartedAt() < republished.Unix() {
		t.Fatalf("republished session: status %q started_at %d, want live from %d", next.GetStatus(), next.GetStartedAt(), republished.Unix())
	}

	// A stream with no projected push session (pull, remote) keeps Mist's view.
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-A", time.Minute)
	if pulled := forwarded(); pulled.GetStatus() != "live" {
		t.Fatalf("stream without a push session reported %q", pulled.GetStatus())
	}
}
