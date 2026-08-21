package grpc

import (
	"context"

	"frameworks/api_billing/internal/database/purserdb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// resolveOperatorTenantID enforces the same cross-tenant guard the invoice
// and usage RPCs use: a user-context call must request its own tenant
// (or omit, in which case the ctx tenant is used); a service-token call
// can pass any tenant_id. Mirrors the pattern in GetUsageRecords +
// GetInvoice. Without this guard a tenant could query another operator's
// revenue.
func resolveOperatorTenantID(ctx context.Context, requestedTenantID string) (string, error) {
	ctxTenantID := middleware.GetTenantID(ctx)
	isServiceCall := middleware.IsServiceCall(ctx)
	if !isServiceCall {
		if ctxTenantID == "" {
			return "", status.Error(codes.PermissionDenied, "tenant context required")
		}
		if requestedTenantID != "" && requestedTenantID != ctxTenantID {
			return "", status.Error(codes.PermissionDenied, "cross-tenant access denied")
		}
		return ctxTenantID, nil
	}
	if requestedTenantID == "" {
		return "", status.Error(codes.InvalidArgument, "tenant_id required")
	}
	return requestedTenantID, nil
}

// GetOperatorRevenue aggregates settled operator-credit states for the
// calling tenant in the requested time range, returning per-cluster sums plus
// totals. Held rows are audit-only and stay out of revenue views until the
// operator is approved for payouts.
func (s *PurserServer) GetOperatorRevenue(ctx context.Context, req *purserpb.GetOperatorRevenueRequest) (*purserpb.GetOperatorRevenueResponse, error) {
	tenantID, err := resolveOperatorTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if req.RangeStart == nil || req.RangeEnd == nil {
		return nil, status.Error(codes.InvalidArgument, "range_start and range_end are required")
	}
	rangeStart := req.RangeStart.AsTime()
	rangeEnd := req.RangeEnd.AsTime()
	if !rangeEnd.After(rangeStart) {
		return nil, status.Error(codes.InvalidArgument, "range_end must be after range_start")
	}

	params := purserdb.GetOperatorRevenueParams{
		TenantID: tenantID, RangeStart: rangeStart, RangeEnd: rangeEnd,
	}
	if req.ClusterId != nil && *req.ClusterId != "" {
		params.FilterCluster = true
		params.ClusterID = *req.ClusterId
	}
	rows, err := purserdb.New(s.db).GetOperatorRevenue(ctx, params)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query operator revenue: %v", err)
	}
	resp := &purserpb.GetOperatorRevenueResponse{}
	for _, row := range rows {
		entry := operatorRevenueEntry(row.ClusterID, row.Currency, row.GrossCents,
			row.PlatformFeeCents, row.PayableCents, row.LineCount)
		resp.Clusters = append(resp.Clusters, entry)
		if resp.Currency == "" {
			resp.Currency = entry.Currency
		}
		resp.TotalGrossCents += entry.GrossCents
		resp.TotalPlatformFeeCents += entry.PlatformFeeCents
		resp.TotalPayableCents += entry.PayableCents
	}
	enrichOperatorClusterNames(ctx, s, resp.Clusters)
	return resp, nil
}

// ListOperatorClusters returns lifetime revenue aggregates for every cluster
// the tenant owns that has at least one non-held ledger row.
func (s *PurserServer) ListOperatorClusters(ctx context.Context, req *purserpb.ListOperatorClustersRequest) (*purserpb.ListOperatorClustersResponse, error) {
	tenantID, err := resolveOperatorTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	rows, err := purserdb.New(s.db).ListOperatorClusters(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query operator clusters: %v", err)
	}
	resp := &purserpb.ListOperatorClustersResponse{}
	for _, row := range rows {
		entry := operatorRevenueEntry(row.ClusterID, row.Currency, row.GrossCents,
			row.PlatformFeeCents, row.PayableCents, row.LineCount)
		resp.Clusters = append(resp.Clusters, entry)
	}
	enrichOperatorClusterNames(ctx, s, resp.Clusters)
	return resp, nil
}

func operatorRevenueEntry(clusterID, currency string, gross, fee, payable int64, lineCount int32) *purserpb.OperatorRevenueByCluster {
	return &purserpb.OperatorRevenueByCluster{
		ClusterId: clusterID, Currency: currency, GrossCents: gross,
		PlatformFeeCents: fee, PayableCents: payable, LineCount: lineCount,
	}
}

// GetOperatorPayouts lists settlement events recorded by the payout workflow.
func (s *PurserServer) GetOperatorPayouts(ctx context.Context, req *purserpb.GetOperatorPayoutsRequest) (*purserpb.GetOperatorPayoutsResponse, error) {
	tenantID, err := resolveOperatorTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	params := purserdb.ListOperatorPayoutsParams{TenantID: tenantID}
	if req.RangeStart != nil {
		params.FilterStart = true
		params.RangeStart = req.RangeStart.AsTime()
	}
	if req.RangeEnd != nil {
		params.FilterEnd = true
		params.RangeEnd = req.RangeEnd.AsTime()
	}
	rows, err := purserdb.New(s.db).ListOperatorPayouts(ctx, params)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query operator payouts: %v", err)
	}
	resp := &purserpb.GetOperatorPayoutsResponse{}
	for _, row := range rows {
		payout := purserpb.OperatorPayout{
			Id: row.ID.String(), Currency: row.Currency, TotalCents: row.TotalCents, Status: row.Status,
			Method: row.Method, ExternalReference: row.ExternalReference, CreatedAt: timestamppb.New(row.CreatedAt),
		}
		if row.PaidAt.Valid {
			payout.PaidAt = timestamppb.New(row.PaidAt.Time)
		}
		resp.Payouts = append(resp.Payouts, &payout)
	}
	return resp, nil
}

// enrichOperatorClusterNames best-effort populates ClusterName from
// Quartermaster. The lookup is one RPC per cluster — fine for the small N
// expected on an operator dashboard. Failures degrade silently to the
// cluster ID.
func enrichOperatorClusterNames(ctx context.Context, s *PurserServer, clusters []*purserpb.OperatorRevenueByCluster) {
	if s.quartermasterClient == nil {
		return
	}
	for _, c := range clusters {
		if c.ClusterId == "" {
			continue
		}
		resp, err := s.quartermasterClient.GetCluster(ctx, c.ClusterId)
		if err != nil || resp == nil || resp.GetCluster() == nil {
			continue
		}
		if name := resp.GetCluster().GetClusterName(); name != "" {
			c.ClusterName = name
		}
	}
}
