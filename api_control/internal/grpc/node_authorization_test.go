package grpc

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func nodeActorContext(authType, role string, permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
}

type nodeManagementFake struct {
	quartermasterpb.UnimplementedNodeServiceServer
	foghornpb.UnimplementedNodeControlServiceServer
	owner *quartermasterpb.NodeOwnerResponse
	err   error
	calls atomic.Int32
}

func (f *nodeManagementFake) GetNodeOwner(context.Context, *quartermasterpb.GetNodeOwnerRequest) (*quartermasterpb.NodeOwnerResponse, error) {
	return f.owner, f.err
}

func (f *nodeManagementFake) SetNodeOperationalMode(context.Context, *foghorncontrolpb.SetNodeModeRequest) (*foghorncontrolpb.SetNodeModeResponse, error) {
	f.calls.Add(1)
	return &foghorncontrolpb.SetNodeModeResponse{}, nil
}

func (f *nodeManagementFake) GetNodeHealth(context.Context, *foghorncontrolpb.GetNodeHealthRequest) (*foghorncontrolpb.GetNodeHealthResponse, error) {
	f.calls.Add(1)
	return &foghorncontrolpb.GetNodeHealthResponse{}, nil
}

func newNodeManagementServer(t *testing.T, owner *quartermasterpb.NodeOwnerResponse, ownerErr error) (*CommodoreServer, *nodeManagementFake) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		owner.FoghornGrpcAddr = proto.String(listener.Addr().String())
	}
	fake := &nodeManagementFake{owner: owner, err: ownerErr}
	rpcServer := grpc.NewServer()
	quartermasterpb.RegisterNodeServiceServer(rpcServer, fake)
	foghornpb.RegisterNodeControlServiceServer(rpcServer, fake)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(func() { rpcServer.Stop(); _ = listener.Close() })
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr: listener.Addr().String(), AllowInsecure: true, Logger: logging.NewLogger(),
		Timeout: 5 * time.Second, ServiceToken: "commodore-service-token", PreferServiceToken: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool := foghornclient.NewPool(foghornclient.PoolConfig{Logger: logging.NewLogger(), AllowInsecure: true, ServiceToken: "commodore-service-token"})
	t.Cleanup(func() { _ = qm.Close(); _ = pool.Close() })
	return &CommodoreServer{logger: logging.NewLogger(), quartermasterClient: qm, foghornPool: pool}, fake
}

func TestNodeManagementAuthorizesActualOwner(t *testing.T) {
	operator := context.WithValue(nodeActorContext("jwt", "member"), ctxkeys.KeyPlatformOperator, true)
	tests := []struct {
		name  string
		ctx   context.Context
		owner *string
		want  codes.Code
	}{
		{"operator tenant node", operator, proto.String("tenant-2"), codes.OK},
		{"operator platform node", operator, nil, codes.OK},
		{"owner own node", nodeActorContext("jwt", "owner"), proto.String("tenant-1"), codes.OK},
		{"owner foreign node", nodeActorContext("jwt", "owner"), proto.String("tenant-2"), codes.PermissionDenied},
		{"member foreign node", nodeActorContext("jwt", "member"), proto.String("tenant-2"), codes.PermissionDenied},
		{"owner platform node", nodeActorContext("jwt", "owner"), nil, codes.PermissionDenied},
		{"unscoped token", nodeActorContext("api_token", "owner"), proto.String("tenant-1"), codes.PermissionDenied},
		{"scoped token", nodeActorContext("api_token", "owner", "infrastructure:write"), proto.String("tenant-1"), codes.OK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, fake := newNodeManagementServer(t, &quartermasterpb.NodeOwnerResponse{ClusterId: "cluster-1", OwnerTenantId: test.owner}, nil)
			_, modeErr := server.SetNodeOperationalMode(test.ctx, &foghorncontrolpb.SetNodeModeRequest{NodeId: "node-1", Mode: "draining"})
			_, healthErr := server.GetNodeHealth(test.ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "node-1"})
			if status.Code(modeErr) != test.want || status.Code(healthErr) != test.want {
				t.Errorf("mode=%v health=%v, want %v", modeErr, healthErr, test.want)
			}
			wantCalls := int32(0)
			if test.want == codes.OK {
				wantCalls = 2
			}
			if fake.calls.Load() != wantCalls {
				t.Fatalf("Foghorn calls = %d, want %d", fake.calls.Load(), wantCalls)
			}
		})
	}
}

func TestNodeManagementHandlersRejectTenantMembersBeforeFanout(t *testing.T) {
	server := &CommodoreServer{}
	ctx := nodeActorContext("jwt", "member")

	if _, err := server.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{NodeId: "node-1", Mode: "draining"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member SetNodeOperationalMode status = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := server.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "node-1"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("member GetNodeHealth status = %v, want PermissionDenied", status.Code(err))
	}
}

func TestNodeManagementRequiresRoleAndAPITokenScope(t *testing.T) {
	tests := []struct {
		name       string
		ctx        context.Context
		permission string
		action     authz.Action
		want       codes.Code
	}{
		{name: "owner session manages", ctx: nodeActorContext("jwt", "owner"), permission: "infrastructure:write", action: authz.ActionManageEdgeCluster, want: codes.OK},
		{name: "member session denied", ctx: nodeActorContext("jwt", "member"), permission: "infrastructure:write", action: authz.ActionManageEdgeCluster, want: codes.PermissionDenied},
		{name: "owner token without scope denied", ctx: nodeActorContext("api_token", "owner"), permission: "infrastructure:write", action: authz.ActionManageEdgeCluster, want: codes.PermissionDenied},
		{name: "owner token writes", ctx: nodeActorContext("api_token", "owner", "infrastructure:write"), permission: "infrastructure:write", action: authz.ActionManageEdgeCluster, want: codes.OK},
		{name: "owner token reads", ctx: nodeActorContext("api_token", "admin", "infrastructure:read"), permission: "infrastructure:read", action: authz.ActionReadPrivateInfrastructure, want: codes.OK},
		{name: "coarse write token", ctx: nodeActorContext("api_token", "owner", "write"), permission: "infrastructure:write", action: authz.ActionManageEdgeCluster, want: codes.PermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := requireNodeAction(tt.ctx, "tenant-1", tt.permission, tt.action)
			if got := status.Code(err); got != tt.want {
				t.Fatalf("status = %v, want %v (err=%v)", got, tt.want, err)
			}
		})
	}
}
