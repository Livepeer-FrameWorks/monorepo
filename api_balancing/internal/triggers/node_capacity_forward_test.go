package triggers

import (
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestNodeCapacityForwardKeepsTheRoutingObservation(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	p := newTestProcessor(t)
	good := &ipcpb.NodeLifecycleUpdate{NodeId: "edge", IsHealthy: true, UpSpeed: 1000, DownSpeed: 2000, ObservedValid: proto.Bool(true)}
	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(good)); err != nil {
		t.Fatal(err)
	}
	bad := &ipcpb.NodeLifecycleUpdate{NodeId: "edge", IsHealthy: true, MistApiReachable: proto.Bool(false), ObservedValid: proto.Bool(false)}
	if _, _, err := p.handleNodeLifecycleUpdate(nodeLifecycleTrigger(bad)); err != nil {
		t.Fatal(err)
	}
	if bad.GetUpSpeed() != 1000 || bad.GetDownSpeed() != 2000 || bad.GetIsHealthy() {
		t.Fatalf("analytics received a different observation from routing: %+v", bad)
	}
}
