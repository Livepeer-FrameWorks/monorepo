package grpc

import (
	"context"
	"database/sql"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const tenantBillingSnapshotsDefaultLimit = 500
const tenantBillingSnapshotsMaxLimit = 1000

// ListTenantBillingSnapshots returns the cross-tenant billing snapshot for
// the platform operator admin surface. Deliberately unscoped: only service
// credentials (no user/tenant identity) may call it — the gateway gates the
// operator and calls with the service token, same contract as Periscope's
// ListTenantActivity.
func (s *PurserServer) ListTenantBillingSnapshots(ctx context.Context, req *purserpb.ListTenantBillingSnapshotsRequest) (*purserpb.ListTenantBillingSnapshotsResponse, error) {
	if !middleware.IsServiceCall(ctx) {
		return nil, status.Error(codes.PermissionDenied, "service credentials required")
	}

	limit := req.GetLimit()
	if limit <= 0 {
		limit = tenantBillingSnapshotsDefaultLimit
	}
	if limit > tenantBillingSnapshotsMaxLimit {
		limit = tenantBillingSnapshotsMaxLimit
	}
	tenantIDs := req.GetTenantIds()
	if tenantIDs == nil {
		tenantIDs = []string{}
	}
	currency := billing.DefaultCurrency()

	// Explicit casts on every parameter site (Yugabyte rejects untyped
	// placeholders reused across contexts). The empty tenant_ids array means
	// "all subscriptions", capped by limit.
	query := `
		SELECT ts.tenant_id, ts.billing_model, ts.status, ts.tier_id, bt.tier_name,
		       ts.trial_ends_at, ts.next_billing_date, ts.created_at,
		       COALESCE(pb.balance_cents, 0) AS prepaid_balance_cents,
		       COALESCE(inv.outstanding, 0) AS outstanding_amount,
		       COALESCE(inv.overdue_count, 0) AS overdue_invoices
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
		LEFT JOIN purser.prepaid_balances pb
		  ON pb.tenant_id = ts.tenant_id AND pb.currency = $1::text
		LEFT JOIN LATERAL (
		  SELECT SUM(i.amount) AS outstanding,
		         COUNT(*) FILTER (WHERE i.status = 'overdue') AS overdue_count
		  FROM purser.billing_invoices i
		  WHERE i.tenant_id = ts.tenant_id AND i.status IN ('pending', 'overdue')
		) inv ON TRUE
		WHERE (cardinality($2::text[]) = 0 OR ts.tenant_id::text = ANY($2::text[]))
		ORDER BY ts.created_at
		LIMIT $3::int
	`

	var snapshots []*purserpb.TenantBillingSnapshot
	err := fwdb.RetryPostgres(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := s.db.QueryContext(ctx, query, currency, pq.Array(tenantIDs), limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		snapshots = snapshots[:0]
		for rows.Next() {
			var snap purserpb.TenantBillingSnapshot
			var trialEndsAt, nextBillingDate, subscribedAt sql.NullTime
			var outstanding sql.NullFloat64
			if err := rows.Scan(
				&snap.TenantId, &snap.BillingModel, &snap.Status, &snap.TierId, &snap.TierName,
				&trialEndsAt, &nextBillingDate, &subscribedAt,
				&snap.PrepaidBalanceCents, &outstanding, &snap.OverdueInvoices,
			); err != nil {
				return err
			}
			if trialEndsAt.Valid {
				snap.TrialEndsAt = timestamppb.New(trialEndsAt.Time)
			}
			if nextBillingDate.Valid {
				snap.NextBillingDate = timestamppb.New(nextBillingDate.Time)
			}
			if subscribedAt.Valid {
				snap.SubscribedAt = timestamppb.New(subscribedAt.Time)
			}
			snap.OutstandingAmount = outstanding.Float64
			snap.Currency = currency
			snapshots = append(snapshots, &snap)
		}
		return rows.Err()
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to list tenant billing snapshots")
		return nil, status.Error(codes.Internal, "failed to list tenant billing snapshots")
	}

	return &purserpb.ListTenantBillingSnapshotsResponse{Snapshots: snapshots}, nil
}
