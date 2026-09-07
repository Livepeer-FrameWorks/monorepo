package grpc

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type tenantBoundAnalyticsRequest interface {
	GetTenantId() string
}

func apiTokenAuthorizationInterceptor() grpcpkg.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpcpkg.UnaryServerInfo, handler grpcpkg.UnaryHandler) (any, error) {
		if ctxkeys.GetAuthType(ctx) != "api_token" {
			return handler(ctx, req)
		}
		if !hasAnalyticsReadPermission(ctxkeys.GetPermissions(ctx)) {
			return nil, status.Error(codes.PermissionDenied, "API token requires analytics:read scope")
		}
		callerTenant := strings.TrimSpace(ctxkeys.GetTenantID(ctx))
		tenantReq, ok := req.(tenantBoundAnalyticsRequest)
		if callerTenant == "" || !ok || strings.TrimSpace(tenantReq.GetTenantId()) != callerTenant {
			return nil, status.Error(codes.PermissionDenied, "API token analytics request must target its authenticated tenant")
		}
		return handler(ctx, req)
	}
}

func hasAnalyticsReadPermission(permissions []string) bool {
	for _, permission := range permissions {
		permission = strings.TrimSpace(permission)
		if permission == "analytics:read" {
			return true
		}
	}
	return false
}
