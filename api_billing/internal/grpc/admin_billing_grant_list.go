package grpc

import (
	"context"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminListBillingGrants reads current arrangements and optional retained history
// for platform operators, with bounded expiry and pagination filters.
func (s *PurserServer) AdminListBillingGrants(ctx context.Context, req *purserpb.AdminListBillingGrantsRequest) (*purserpb.AdminListBillingGrantsResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 || req.GetOffset() < 0 || req.GetExpiringWithinSeconds() < 0 || req.GetExpiringWithinSeconds() > 10*366*24*60*60 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 1..500; offset must be non-negative; expiry window must be 0..10 years")
	}
	rows, err := purserdb.New(s.db).ListOperatorBillingGrants(ctx, purserdb.ListOperatorBillingGrantsParams{
		IncludeExpired: req.GetIncludeExpired(), ExpiringSeconds: req.GetExpiringWithinSeconds(), RowLimit: int64(limit) + 1, RowOffset: int64(req.GetOffset()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list grants: %v", err)
	}
	resp := &purserpb.AdminListBillingGrantsResponse{HasMore: len(rows) > int(limit)}
	if resp.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		grant := &purserpb.OperatorBillingGrant{SubscriptionId: row.SubscriptionID, WaiveUsage: row.WaiveUsage, ExpiryError: row.ExpiryError.String, ExpiryFailures: row.ExpiryFailures,
			Collection: row.Collection, Reason: row.Reason, GrantedBy: row.GrantedBy.String, GrantedAt: timestamppb.New(row.GrantedAt),
			Active: row.Active, FallbackTierName: row.FallbackTierName}
		if row.BasePrice.Valid {
			value := row.BasePrice.String
			grant.BasePrice = &value
		}
		if row.ExpiresAt.Valid {
			grant.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
		}
		if row.EndedAt.Valid {
			grant.EndedAt = timestamppb.New(row.EndedAt.Time)
		}
		resp.Grants = append(resp.Grants, &purserpb.AdminBillingGrantEntry{TenantId: row.TenantID, TierName: row.TierName, Grant: grant})
	}
	return resp, nil
}
