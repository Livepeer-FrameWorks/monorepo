package control

import (
	"errors"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
)

func TestDVRDispatchReservesBothLinkDirections(t *testing.T) {
	ensureRegistry(t)
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	for _, node := range []string{"source", "storage", "worker"} {
		sm.SetNodeInfo(node, "https://edge.test", true, nil, nil, "", "", nil)
	}
	if err := sm.UpdateStreamFromBuffer("live+stream", "stream", "source", "tenant", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	stream := &fakeControlStream{}
	registry.conns["storage"] = &conn{stream: stream, protocolVersion: ProcessingTenantEchoProtocolMin}
	if err := SendDVRStart("storage", &ipcpb.DVRStartRequest{DvrHash: "recording", InternalName: "stream", SourceRuntimeName: "live+stream"}); err != nil {
		t.Fatal(err)
	}
	if n := sm.GetNodeState("source"); n == nil || n.AddBandwidth == 0 {
		t.Errorf("DVR source reserved no egress: %+v", n)
	}
	if n := sm.GetNodeState("storage"); n == nil || n.IngressReservedBytesPerSec == 0 {
		t.Errorf("DVR storage reserved no ingress: %+v", n)
	}
}

func TestProcessingDispatchReservesSourceAndWorker(t *testing.T) {
	ensureRegistry(t)
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	for _, node := range []string{"source", "storage", "worker"} {
		sm.SetNodeInfo(node, "https://edge.test", true, nil, nil, "", "", nil)
	}
	stream := &fakeControlStream{}
	registry.conns["worker"] = &conn{stream: stream, protocolVersion: ProcessingTenantEchoProtocolMin}
	req := &ipcpb.ProcessingJobRequest{JobId: "job", InternalName: "clip", Params: map[string]string{"source_node_id": "source", "source_stream_name": "live+stream"}}
	if err := SendProcessingJob("worker", req); err != nil {
		t.Fatal(err)
	}
	if n := sm.GetNodeState("source"); n == nil || n.AddBandwidth == 0 {
		t.Errorf("clip source reserved no egress: %+v", n)
	}
	if n := sm.GetNodeState("worker"); n == nil || n.IngressReservedBytesPerSec == 0 {
		t.Errorf("clip worker reserved no ingress: %+v", n)
	}
}

func TestReviewSameNodeRecordingDoesNotReserveNICBandwidth(t *testing.T) {
	ensureRegistry(t)
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	for _, node := range []string{"source", "storage", "worker"} {
		sm.SetNodeInfo(node, "https://edge.test", true, nil, nil, "", "", nil)
	}
	if err := sm.UpdateStreamFromBuffer("live+local", "local", "same-node", "tenant", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.SetNodeCapacity("same-node", &ipcpb.NodeBandwidthCapacity{EgressBudgetBytesPerSec: 1, IngressBudgetBytesPerSec: 1})
	stream := &fakeControlStream{}
	registry.conns["same-node"] = &conn{stream: stream, protocolVersion: ProcessingTenantEchoProtocolMin}
	err := SendDVRStart("same-node", &ipcpb.DVRStartRequest{DvrHash: "local-recording", InternalName: "local", SourceRuntimeName: "live+local"})
	if err != nil {
		t.Fatalf("local recording refused despite requiring no NIC traffic: %v", err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetDvrStartRequest() == nil {
		t.Fatal("local recording was not dispatched")
	}
	if n := sm.GetNodeState("same-node"); n.AddBandwidth != 0 || n.IngressReservedBytesPerSec != 0 {
		t.Fatalf("local recording reserved fictitious NIC traffic: %d/%d", n.AddBandwidth, n.IngressReservedBytesPerSec)
	}
}

func TestReviewRound2FailedRecordingSendReleasesBandwidth(t *testing.T) {
	ensureRegistry(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	for _, node := range []string{"source", "storage"} {
		sm.SetNodeInfo(node, "https://edge.test", true, nil, nil, "", "", nil)
	}
	if err := sm.UpdateStreamFromBuffer("live+failed", "failed", "source", "tenant", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	delete(registry.conns, "storage")
	if err := SendDVRStart("storage", &ipcpb.DVRStartRequest{DvrHash: "failed-send", InternalName: "failed"}); err == nil {
		t.Fatal("fixture unexpectedly sent recording")
	}
	for _, node := range []string{"source", "storage"} {
		n := sm.GetNodeState(node)
		if n.AddBandwidth != 0 || n.IngressReservedBytesPerSec != 0 {
			t.Errorf("failed send retained %s bandwidth: %d/%d", node, n.AddBandwidth, n.IngressReservedBytesPerSec)
		}
	}
}

func TestProcessingSendFailureReleasesPendingBandwidth(t *testing.T) {
	for _, connected := range []bool{false, true} {
		name := "disconnected"
		if connected {
			name = "stream-write-failure"
		}
		t.Run(name, func(t *testing.T) {
			ensureRegistry(t)
			setCommandRelay(t, nil)
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			for _, node := range []string{"source", "worker"} {
				sm.SetNodeInfo(node, "https://edge.test", true, nil, nil, "", "", nil)
			}
			wantErr := ErrNotConnected
			if connected {
				wantErr = errors.New("processing stream write failed")
				registry.conns["worker"] = &conn{stream: &fakeControlStream{err: wantErr}, protocolVersion: ProcessingTenantEchoProtocolMin}
			}
			req := &ipcpb.ProcessingJobRequest{JobId: "failed-send", InternalName: "clip", Params: map[string]string{"source_node_id": "source", "source_stream_name": "live+stream"}}
			if err := SendProcessingJob("worker", req); !errors.Is(err, wantErr) {
				t.Fatalf("dispatch error = %v, want %v", err, wantErr)
			}
			for _, node := range []string{"source", "worker"} {
				n := sm.GetNodeState(node)
				if n.AddBandwidth != 0 || n.IngressReservedBytesPerSec != 0 {
					t.Errorf("failed processing send retained %s bandwidth: %d/%d", node, n.AddBandwidth, n.IngressReservedBytesPerSec)
				}
			}
		})
	}
}

func TestFailedProcessingRetryPreservesActiveBandwidth(t *testing.T) {
	ensureRegistry(t)
	setCommandRelay(t, nil)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	for _, node := range []string{"source", "worker"} {
		sm.SetNodeInfo(node, "https://edge.test", true, nil, nil, "", "", nil)
	}
	stream := &fakeControlStream{}
	registry.conns["worker"] = &conn{stream: stream, protocolVersion: ProcessingTenantEchoProtocolMin}
	req := &ipcpb.ProcessingJobRequest{JobId: "active-job", InternalName: "clip", Params: map[string]string{"source_node_id": "source", "source_stream_name": "live+stream"}}
	if err := SendProcessingJob("worker", req); err != nil {
		t.Fatal(err)
	}
	egress := sm.GetNodeState("source").AddBandwidth
	ingress := sm.GetNodeState("worker").IngressReservedBytesPerSec
	if egress == 0 || ingress == 0 {
		t.Fatal("successful dispatch did not hold both bandwidth legs")
	}
	stream.err = errors.New("retry stream write failed")
	if err := SendProcessingJob("worker", req); !errors.Is(err, stream.err) {
		t.Fatalf("retry error = %v, want %v", err, stream.err)
	}
	if got := sm.GetNodeState("source").AddBandwidth; got != egress {
		t.Fatalf("failed retry released active source bandwidth: %d, want %d", got, egress)
	}
	if got := sm.GetNodeState("worker").IngressReservedBytesPerSec; got != ingress {
		t.Fatalf("failed retry released active worker bandwidth: %d, want %d", got, ingress)
	}
}
