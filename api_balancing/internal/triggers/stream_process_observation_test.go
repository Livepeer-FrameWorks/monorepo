package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestStreamLifecyclePreservesProcessObservationForAuthenticatedNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	p := &Processor{logger: logging.NewLogger()}
	observation := &ipcpb.MistStreamProcessObservation{
		RuntimeName: "live+demo", BufferPid: proto.Int64(90), SourcePidsKnown: true, SourcePids: []int64{21},
		ReadStartedUnixMillis: 100, ReadCompletedUnixMillis: 110,
	}
	trigger := &ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: "node-A", StreamId: proto.String("b3b1c1de-0000-4000-8000-000000000001"),
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
			TenantId: proto.String("tenant-1"), InternalName: "demo", Status: "live", TotalInputs: proto.Uint32(1), ProcessObservation: observation,
		}},
	}
	if _, _, err := p.handleStreamLifecycleUpdate(trigger); err != nil {
		t.Fatal(err)
	}
	if got := sm.GetStreamInstances("demo")["node-A"].ProcessObservation; !proto.Equal(got, observation) {
		t.Fatalf("process census not retained: %v", got)
	}
	trigger.GetStreamLifecycleUpdate().NodeId = "forged-node"
	if _, _, err := p.handleStreamLifecycleUpdate(trigger); err == nil {
		t.Fatal("node mismatch was accepted")
	}
	if _, exists := sm.GetStreamInstances("demo")["forged-node"]; exists {
		t.Fatal("payload node was allowed to create an instance")
	}
}
