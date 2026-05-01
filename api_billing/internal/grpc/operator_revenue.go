package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"
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

// GetOperatorRevenue aggregates the operator_credit_ledger for the calling
// tenant in the requested time range, returning per-cluster sums plus
// totals. Currency is taken from the first row; mixed-currency operators are
// rare but supported via per-cluster currency.
func (s *PurserServer) GetOperatorRevenue(ctx context.Context, req *pb.GetOperatorRevenueRequest) (*pb.GetOperatorRevenueResponse, error) {
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

	args := []any{tenantID, rangeStart, rangeEnd}
	clusterFilter := ""
	if req.ClusterId != nil && *req.ClusterId != "" {
		clusterFilter = " AND cluster_id = $4"
		args = append(args, *req.ClusterId)
	}
	q := fmt.Sprintf(`
		SELECT cluster_id, currency,
		       SUM(gross_cents)::bigint,
		       SUM(platform_fee_cents)::bigint,
		       SUM(payable_cents)::bigint,
		       COUNT(*) FILTER (WHERE entry_type = 'accrual')::int
		FROM purser.operator_credit_ledger
		WHERE cluster_owner_tenant_id = $1
		  AND period_start < $3
		  AND period_end > $2
		  %s
		GROUP BY cluster_id, currency
		ORDER BY cluster_id
	`, clusterFilter)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query operator revenue: %v", err)
	}
	defer rows.Close()

	resp := &pb.GetOperatorRevenueResponse{}
	for rows.Next() {
		entry := &pb.OperatorRevenueByCluster{}
		if err := rows.Scan(&entry.ClusterId, &entry.Currency,
			&entry.GrossCents, &entry.PlatformFeeCents, &entry.PayableCents, &entry.LineCount); err != nil {
			return nil, status.Errorf(codes.Internal, "scan operator revenue row: %v", err)
		}
		resp.Clusters = append(resp.Clusters, entry)
		if resp.Currency == "" {
			resp.Currency = entry.Currency
		}
		resp.TotalGrossCents += entry.GrossCents
		resp.TotalPlatformFeeCents += entry.PlatformFeeCents
		resp.TotalPayableCents += entry.PayableCents
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate operator revenue rows: %v", err)
	}

	enrichOperatorClusterNames(ctx, s, resp.Clusters)
	return resp, nil
}

// ListOperatorClusters returns lifetime aggregates for every cluster the
// tenant owns that has at least one ledger row.
func (s *PurserServer) ListOperatorClusters(ctx context.Context, req *pb.ListOperatorClustersRequest) (*pb.ListOperatorClustersResponse, error) {
	tenantID, err := resolveOperatorTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT cluster_id, currency,
		       SUM(gross_cents)::bigint,
		       SUM(platform_fee_cents)::bigint,
		       SUM(payable_cents)::bigint,
		       COUNT(*) FILTER (WHERE entry_type = 'accrual')::int
		FROM purser.operator_credit_ledger
		WHERE cluster_owner_tenant_id = $1
		GROUP BY cluster_id, currency
		ORDER BY cluster_id
	`, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query operator clusters: %v", err)
	}
	defer rows.Close()

	resp := &pb.ListOperatorClustersResponse{}
	for rows.Next() {
		entry := &pb.OperatorRevenueByCluster{}
		if err := rows.Scan(&entry.ClusterId, &entry.Currency,
			&entry.GrossCents, &entry.PlatformFeeCents, &entry.PayableCents, &entry.LineCount); err != nil {
			return nil, status.Errorf(codes.Internal, "scan operator cluster row: %v", err)
		}
		resp.Clusters = append(resp.Clusters, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate operator cluster rows: %v", err)
	}
	enrichOperatorClusterNames(ctx, s, resp.Clusters)
	return resp, nil
}

// GetOperatorPayouts lists settlement events recorded by the payout workflow.
func (s *PurserServer) GetOperatorPayouts(ctx context.Context, req *pb.GetOperatorPayoutsRequest) (*pb.GetOperatorPayoutsResponse, error) {
	tenantID, err := resolveOperatorTenantID(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	args := []any{tenantID}
	clauses := []string{"cluster_owner_tenant_id = $1"}
	if req.RangeStart != nil {
		clauses = append(clauses, fmt.Sprintf("created_at >= $%d", len(args)+1))
		args = append(args, req.RangeStart.AsTime())
	}
	if req.RangeEnd != nil {
		clauses = append(clauses, fmt.Sprintf("created_at < $%d", len(args)+1))
		args = append(args, req.RangeEnd.AsTime())
	}
	q := fmt.Sprintf(`
		SELECT id, currency, total_cents, status,
		       COALESCE(method, ''), COALESCE(external_reference, ''),
		       created_at, paid_at
		FROM purser.operator_payouts
		WHERE %s
		ORDER BY created_at DESC
	`, joinAndClauses(clauses))

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query operator payouts: %v", err)
	}
	defer rows.Close()

	resp := &pb.GetOperatorPayoutsResponse{}
	for rows.Next() {
		var (
			payout    pb.OperatorPayout
			createdAt time.Time
			paidAt    sql.NullTime
		)
		if err := rows.Scan(&payout.Id, &payout.Currency, &payout.TotalCents, &payout.Status,
			&payout.Method, &payout.ExternalReference, &createdAt, &paidAt); err != nil {
			return nil, status.Errorf(codes.Internal, "scan operator payout row: %v", err)
		}
		payout.CreatedAt = timestamppb.New(createdAt)
		if paidAt.Valid {
			payout.PaidAt = timestamppb.New(paidAt.Time)
		}
		resp.Payouts = append(resp.Payouts, &payout)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "iterate operator payout rows: %v", err)
	}
	return resp, nil
}

// enrichOperatorClusterNames best-effort populates ClusterName from
// Quartermaster. The lookup is one RPC per cluster — fine for the small N
// expected on an operator dashboard. Failures degrade silently to the
// cluster ID.
func enrichOperatorClusterNames(ctx context.Context, s *PurserServer, clusters []*pb.OperatorRevenueByCluster) {
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

func joinAndClauses(clauses []string) string {
	if len(clauses) == 0 {
		return "true"
	}
	out := clauses[0]
	for _, c := range clauses[1:] {
		out += " AND " + c
	}
	return out
}
