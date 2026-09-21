package resolvers

import (
	"context"

	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func tenantIDFromContext(ctx context.Context) string {
	if user := middleware.GetUserFromContext(ctx); user != nil && user.TenantID != "" {
		return user.TenantID
	}
	return ctxkeys.GetTenantID(ctx)
}

func userIDFromContext(ctx context.Context) string {
	if user := middleware.GetUserFromContext(ctx); user != nil && user.UserID != "" {
		return user.UserID
	}
	return ctxkeys.GetUserID(ctx)
}
