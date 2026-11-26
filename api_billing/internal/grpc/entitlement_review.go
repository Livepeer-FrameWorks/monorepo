package grpc

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UpdateSubscriptionWithEntitlementReview fails closed on servers that do not
// implement the reviewed-map contract; callers must not fall back to an unchecked edit.
func (s *PurserServer) UpdateSubscriptionWithEntitlementReview(ctx context.Context, req *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	if !middleware.IsServiceCall(ctx) && !ctxkeys.IsPlatformOperator(ctx) {
		return nil, status.Error(codes.PermissionDenied, "reviewed subscription terms require platform operator access")
	}
	if !req.GetCheckEntitlementOverrides() || strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "an entitlement review and audit reason are required")
	}
	return s.UpdateSubscription(ctx, req)
}
