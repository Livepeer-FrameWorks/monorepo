package grpc

import (
	"context"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"

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

	var snapshots []*purserpb.TenantBillingSnapshot
	err := fwdb.RetryPostgres(ctx, fwdb.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		rows, err := purserdb.New(s.db).ListTenantBillingSnapshots(ctx, purserdb.ListTenantBillingSnapshotsParams{
			Currency: currency, TenantIds: tenantIDs, RowLimit: limit,
		})
		if err != nil {
			return err
		}
		snapshots = snapshots[:0]
		for _, row := range rows {
			snap := purserpb.TenantBillingSnapshot{
				TenantId: row.TenantID.String(), BillingModel: row.BillingModel, Status: row.Status,
				TierId: row.TierID.String(), TierName: row.TierName,
				PrepaidBalanceCents: row.PrepaidBalanceCents, OutstandingAmount: row.OutstandingAmount,
				OverdueInvoices: row.OverdueInvoices, Currency: currency,
			}
			if row.TrialEndsAt.Valid {
				snap.TrialEndsAt = timestamppb.New(row.TrialEndsAt.Time)
			}
			if row.NextBillingDate.Valid {
				snap.NextBillingDate = timestamppb.New(row.NextBillingDate.Time)
			}
			if row.CreatedAt.Valid {
				snap.SubscribedAt = timestamppb.New(row.CreatedAt.Time)
			}
			snapshots = append(snapshots, &snap)
		}
		return nil
	})
	if err != nil {
		s.logger.WithError(err).Error("Failed to list tenant billing snapshots")
		return nil, status.Error(codes.Internal, "failed to list tenant billing snapshots")
	}

	return &purserpb.ListTenantBillingSnapshotsResponse{Snapshots: snapshots}, nil
}
