package grpc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type capacityManagementFake struct {
	nodeManagementFake
	capacity              quartermasterpb.NodeCapacity
	saved                 *quartermasterpb.SetNodeCapacityRequest
	savedRead             *quartermasterpb.GetNodeCapacityRequest
	writes, reads, pushes atomic.Int32
	pushErr               error
}

func (f *capacityManagementFake) SetNodeCapacity(_ context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	f.writes.Add(1)
	f.saved = proto.Clone(req).(*quartermasterpb.SetNodeCapacityRequest)
	return &f.capacity, nil
}
func (f *capacityManagementFake) GetNodeCapacity(_ context.Context, req *quartermasterpb.GetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	f.reads.Add(1)
	f.savedRead = req
	return &f.capacity, nil
}
func (f *capacityManagementFake) PushNodeCapacity(context.Context, *foghornpb.PushNodeCapacityRequest) (*foghornpb.PushNodeCapacityResponse, error) {
	f.pushes.Add(1)
	return &foghornpb.PushNodeCapacityResponse{}, f.pushErr
}
func newCapacityManagementServer(t *testing.T, owner *string, pushErr error) (*CommodoreServer, *capacityManagementFake) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &capacityManagementFake{nodeManagementFake: nodeManagementFake{owner: &quartermasterpb.NodeOwnerResponse{ClusterId: "cluster", OwnerTenantId: owner, FoghornGrpcAddr: proto.String(listener.Addr().String())}}, capacity: quartermasterpb.NodeCapacity{Revision: 3, EffectiveEgressBytesPerSec: 250000000, EffectiveIngressBytesPerSec: 125000000}, pushErr: pushErr}
	rpcServer := grpc.NewServer()
	quartermasterpb.RegisterNodeServiceServer(rpcServer, fake)
	foghornpb.RegisterNodeControlServiceServer(rpcServer, fake)
	go func() { _ = rpcServer.Serve(listener) }()
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(), Timeout: 5 * time.Second, ServiceToken: "test", PreferServiceToken: true})
	if err != nil {
		t.Fatal(err)
	}
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, ServiceToken: "test"})
	t.Cleanup(func() { rpcServer.Stop(); _ = listener.Close(); _ = qm.Close(); _ = pool.Close() })
	return &CommodoreServer{logger: logging.NewLogger(), quartermasterClient: qm, foghornPool: pool, tokenHasher: testTokenHasher(t)}, fake
}
func TestNodeCapacityPersistsWhenLivePushFails(t *testing.T) {
	server, fake := newCapacityManagementServer(t, proto.String("tenant-1"), status.Error(codes.Unavailable, "node offline"))
	ctx := nodeActorContext("jwt", "owner")
	request := &quartermasterpb.SetNodeCapacityRequest{NodeId: "node", EgressBudgetBytesPerSec: proto.Uint64(250000000), ExpectedRevision: 2, Actor: &commonpb.RequestActor{UserId: "spoof"}}
	result, err := server.SetNodeCapacity(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetRevision() != 3 || fake.writes.Load() != 1 || fake.pushes.Load() != 1 {
		t.Fatalf("capacity write/push not performed: result=%v writes=%d pushes=%d", result, fake.writes.Load(), fake.pushes.Load())
	}
	if fake.saved.ExpectedOwnerTenantId == nil || fake.saved.GetExpectedOwnerTenantId() != "tenant-1" {
		t.Errorf("write lost checked owner: %v", fake.saved)
	}
	if fake.saved.GetActor().GetUserId() != "user-1" || fake.saved.GetExpectedRevision() != 2 || fake.saved.IngressBudgetBytesPerSec != nil {
		t.Errorf("CAS, reset or audit actor lost: %v", fake.saved)
	}
	if request.GetActor().GetUserId() != "spoof" {
		t.Fatal("caller request mutated")
	}
}
func TestNodeCapacityActionsCheckActualOwnerAndScope(t *testing.T) {
	operator := context.WithValue(nodeActorContext("jwt", "member"), ctxkeys.KeyPlatformOperator, true)
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		owner *string
		want  codes.Code
	}{
		{"owner", nodeActorContext("jwt", "owner"), proto.String("tenant-1"), codes.OK},
		{"foreign owner", nodeActorContext("jwt", "owner"), proto.String("tenant-2"), codes.PermissionDenied},
		{"platform operator", operator, nil, codes.OK},
		{"unscoped token", nodeActorContext("api_token", "owner"), proto.String("tenant-1"), codes.PermissionDenied},
		{"scoped token", nodeActorContext("api_token", "owner", "infrastructure:write"), proto.String("tenant-1"), codes.OK},
		{"member", nodeActorContext("jwt", "member"), proto.String("tenant-1"), codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, fake := newCapacityManagementServer(t, tc.owner, nil)
			_, writeErr := server.SetNodeCapacity(tc.ctx, &quartermasterpb.SetNodeCapacityRequest{NodeId: "node"})
			_, readErr := server.GetNodeCapacity(tc.ctx, &quartermasterpb.GetNodeCapacityRequest{NodeId: "node"})
			if status.Code(writeErr) != tc.want || status.Code(readErr) != tc.want {
				t.Fatalf("write=%v read=%v want=%v", writeErr, readErr, tc.want)
			}
			if tc.want == codes.OK {
				if fake.savedRead.ExpectedOwnerTenantId == nil || fake.savedRead.GetExpectedOwnerTenantId() != fake.owner.GetOwnerTenantId() {
					t.Errorf("read lost checked owner: %+v", fake.savedRead)
				}
			}
			if tc.want != codes.OK && (fake.writes.Load() != 0 || fake.reads.Load() != 0 || fake.pushes.Load() != 0) {
				t.Fatal("unauthorized capacity fanout")
			}
		})
	}
}
