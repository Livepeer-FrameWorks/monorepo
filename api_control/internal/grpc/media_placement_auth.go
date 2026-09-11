package grpc

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requireMediaPlacementAccess(ctx context.Context, tenantID string, manage bool) error {
	permission, action := "placement:read", authz.ActionReadMediaPlacement
	if manage {
		permission, action = "placement:write", authz.ActionManageMediaPlacement
	}
	if ctxkeys.GetAuthType(ctx) == "api_token" && !hasDelegatedPermission(ctxkeys.GetPermissions(ctx), permission) {
		return status.Errorf(codes.PermissionDenied, "API token requires %s scope", permission)
	}
	decision := authz.Default.Can(ctx, authz.Identity{
		UserID: ctxkeys.GetUserID(ctx), TenantID: ctxkeys.GetTenantID(ctx), Role: ctxkeys.GetRole(ctx),
		Permissions: ctxkeys.GetPermissions(ctx), PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
	}, action, authz.Resource{OwnerTenantID: tenantID})
	if !decision.Allow {
		return status.Error(codes.PermissionDenied, decision.Reason)
	}
	return nil
}
