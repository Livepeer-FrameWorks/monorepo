package middleware

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RequirePlatformOperatorJWT admits only an interactive JWT caller holding the
// platform_operator grant. Operator diagnostics and remediation name the
// operator in their audit trail, so a service-token call (no user identity) is
// refused even though it is otherwise trusted, and so are API-token and other
// delegated credentials.
func RequirePlatformOperatorJWT(ctx context.Context) error {
	if ctxkeys.GetAuthType(ctx) != "jwt" || ctxkeys.GetUserID(ctx) == "" {
		return status.Error(codes.PermissionDenied, "platform operator session required")
	}
	id := authz.Identity{
		UserID:           ctxkeys.GetUserID(ctx),
		TenantID:         ctxkeys.GetTenantID(ctx),
		Role:             ctxkeys.GetRole(ctx),
		PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
	}
	if decision := authz.Default.Can(ctx, id, authz.ActionAccessPlatformAdmin, authz.Resource{}); !decision.Allow {
		return status.Error(codes.PermissionDenied, decision.Reason)
	}
	return nil
}
