package control

import (
	"context"
	"strconv"
	"testing"

	"frameworks/api_balancing/internal/state"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestProcessingDispatchRequiresTenantEchoProtocol(t *testing.T) {
	for _, version := range []int32{10, 12, 13} {
		t.Run(strconv.Itoa(int(version)), func(t *testing.T) {
			ensureRegistry(t)
			stream := &fakeControlStream{}
			registry.conns["worker"] = &conn{stream: stream, protocolVersion: version}
			err := SendLocalProcessingJob("worker", &ipcpb.ProcessingJobRequest{JobId: "job", TenantId: "tenant"})
			if version < 13 {
				if status.Code(err) != codes.FailedPrecondition || len(stream.sent) != 0 {
					t.Fatalf("protocol %d cannot echo tenant in results but received work: sent=%d err=%v", version, len(stream.sent), err)
				}
			} else if err != nil || len(stream.sent) != 1 {
				t.Fatalf("capable protocol %d did not receive work: sent=%d err=%v", version, len(stream.sent), err)
			}
		})
	}
}

func TestProcessingProtocolUsesFencedRemoteOwner(t *testing.T) {
	ensureRegistry(t)
	store, redis := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "self", "self:9090", &mockRelayPool{}))
	for _, version := range []int32{10, 13} {
		node := strconv.Itoa(int(version))
		ok, err := store.AcquireConnOwnerFenced(context.Background(), node, "peer", "peer:9090", 1, state.ConnProtocol{ProtocolVersion: version, ClusterID: "cluster"})
		if err != nil || !ok {
			t.Fatalf("register remote owner: %v", err)
		}
		supported, known := NodeProcessingProtocolOK(t.Context(), node)
		if !known || supported != (version >= 13) {
			t.Fatalf("remote version %d: supported=%v known=%v", version, supported, known)
		}
	}
	redis.Close()
	if supported, known := NodeProcessingProtocolOK(t.Context(), "13"); supported || !known {
		t.Fatal("failed ownership read allowed processing dispatch")
	}
}

func TestProcessingRelayProtocolRefusalKeepsConnectionOwner(t *testing.T) {
	ensureRegistry(t)
	store, _ := newTestStore(t)
	ctx := t.Context()
	ok, err := store.AcquireConnOwnerFenced(ctx, "edge", "peer", "peer:9090", 1, state.ConnProtocol{ProtocolVersion: 12})
	if err != nil || !ok {
		t.Fatalf("register owner: %v", err)
	}
	before, err := store.GetConnOwner(ctx, "edge")
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeFoghornRelayClient{err: status.Error(codes.FailedPrecondition, "edge sidecar upgrade required for processing result tenant binding")}
	relay := buildRelay(t, store, "self", "self:9090", &mockRelayPool{client: &mockRelayClient{relay: client}})
	request := &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "edge", Command: &foghornrelaypb.ForwardCommandRequest_ProcessingJob{ProcessingJob: &ipcpb.ProcessingJobRequest{JobId: "job", TenantId: "tenant"}}}
	setCommandRelay(t, relay)
	err = SendProcessingJob("edge", request.GetProcessingJob())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("lost processing refusal code: %v", err)
	}
	after, readErr := store.GetConnOwner(ctx, "edge")
	if readErr != nil || after != before {
		t.Fatalf("valid owner evicted after a protocol refusal: before=%+v after=%+v error=%v", before, after, readErr)
	}
}
