package foghorn

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pendingBootstrapReplicaClient struct {
	calls      int
	allPending bool
	request    *foghornpb.PreRegisterEdgeRequest
	t          *testing.T
}

func (c *pendingBootstrapReplicaClient) PreRegisterEdge(_ context.Context, req *foghornpb.PreRegisterEdgeRequest, _ ...grpc.CallOption) (*foghornpb.PreRegisterEdgeResponse, error) {
	c.calls++
	if c.request == nil {
		c.request = req
	} else if c.request != req {
		c.t.Error("retry changed advisory identity")
	}
	if c.allPending || c.calls == 1 {
		return nil, grpcutil.BootstrapPendingError(grpcutil.BootstrapPublicIdentityPending, time.Second)
	}
	return &foghornpb.PreRegisterEdgeResponse{NodeId: req.GetPreferredNodeId()}, nil
}

func TestBootstrapPendingReplicaDoesNotStrandReadyReplica(t *testing.T) {
	edge := &pendingBootstrapReplicaClient{t: t}
	client := &GRPCClient{edge: edge, bootstrap: true, timeout: 2 * time.Second}
	response, err := client.PreRegisterEdge(t.Context(), &foghornpb.PreRegisterEdgeRequest{EnrollmentToken: "fixed-enrollment", PreferredNodeId: "fixed-node"})
	if err != nil || response.GetNodeId() != "fixed-node" || edge.calls < 2 {
		t.Fatalf("pending member stranded another ready replica: response=%v calls=%d err=%v", response, edge.calls, err)
	}
}

func TestBootstrapAllPendingRetainsTypedStateButHonorsCallerDeadline(t *testing.T) {
	for _, callerBound := range []bool{false, true} {
		t.Run(map[bool]string{false: "own operation ends", true: "caller ends"}[callerBound], func(t *testing.T) {
			edge := &pendingBootstrapReplicaClient{t: t, allPending: true}
			client := &GRPCClient{edge: edge, bootstrap: true, timeout: 300 * time.Millisecond}
			ctx := t.Context()
			if callerBound {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			started := time.Now()
			_, err := client.PreRegisterEdge(ctx, &foghornpb.PreRegisterEdgeRequest{PreferredNodeId: "fixed-node"})
			_, _, pending := grpcutil.BootstrapState(err)
			if callerBound && (pending || status.Code(err) != codes.DeadlineExceeded) {
				t.Fatalf("pending state hid caller deadline: %v", err)
			}
			if !callerBound && !pending {
				t.Fatalf("operation discarded safe pending state: %v", err)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("pending retries exceeded their operation")
			}
		})
	}
}
