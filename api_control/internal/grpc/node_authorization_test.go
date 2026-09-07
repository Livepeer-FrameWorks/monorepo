package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func nodeActorContext(authType, role string, permissions ...string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, authType)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	return context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
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
