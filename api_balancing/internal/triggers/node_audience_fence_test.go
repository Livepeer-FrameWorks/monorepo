package triggers

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func TestStaleNodeReportCannotClearNewAudienceThroughAbsence(t *testing.T) {
	f := newLifecyclePullFixture(t)
	report := func(fence, seq int64, streams map[string]*ipcpb.StreamData) {
		t.Helper()
		if _, _, err := f.processor.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{NodeId: "edge", IsHealthy: true, Timestamp: time.Now().Unix(), ArtifactsConnectionFence: fence, ArtifactsReportSeq: seq, Streams: streams})); err != nil {
			t.Fatal(err)
		}
	}
	report(5, 10, map[string]*ipcpb.StreamData{"stream": {Total: 3, AudienceViewers: proto.Uint32(2), Inputs: 1}})
	if got := f.state.GetNodeActiveViewers("edge"); got != 2 {
		t.Fatalf("new owner census=%d, want 2", got)
	}
	report(4, 99, nil)
	if got := f.state.GetNodeActiveViewers("edge"); got != 2 {
		t.Fatalf("old owner's empty report cleared new census to %d", got)
	}
}
