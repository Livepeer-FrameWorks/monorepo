package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AdminRecoverTierUpgrade records a durable locate-only recovery. A confirmed
// provider purchase finishes billing; an unapplied request can be cancelled.
func (s *PurserServer) AdminRecoverTierUpgrade(ctx context.Context, r *purserpb.AdminRecoverTierUpgradeRequest) (*purserpb.AdminRecoverTierUpgradeResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if _, err := uuid.Parse(r.GetUpgradeId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "upgrade_id must be a UUID")
	}
	if r.GetConfirmed() && strings.TrimSpace(r.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "tier upgrade recovery is unavailable")
	}
	if r.GetConfirmed() {
		err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			locked, err := subscriptions.LockChange(ctx, tx, r.GetTenantId(), subscriptions.PreservePricing)
			if err != nil {
				return err
			}
			q := purserdb.New(tx)
			if _, err = q.GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: r.GetTenantId(), ID: r.GetUpgradeId()}); err != nil {
				return err
			}
			rows, err := q.RequestTierUpgradeRecovery(ctx, purserdb.RequestTierUpgradeRecoveryParams{TenantID: r.GetTenantId(), ID: r.GetUpgradeId(), ActorID: ctxkeys.GetUserID(ctx), Reason: strings.TrimSpace(r.GetReason())})
			if err != nil {
				return err
			}
			if rows > 0 {
				if err = billingevents.NewAndEnqueue(ctx, tx, r.GetTenantId(), locked.Current.SubscriptionID, &internalv1.SubscriptionUpdated{SubscriptionId: locked.Current.SubscriptionID, ChangedFields: []string{"tier_upgrade_recovery_requested"}}, s.domainActor(ctx)); err != nil {
					return err
				}
			}
			return q.EnqueueTierUpgrade(ctx, purserdb.EnqueueTierUpgradeParams{TenantID: r.GetTenantId(), ID: r.GetUpgradeId()})
		})
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tenant tier upgrade not found")
		}
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "record upgrade recovery: %v", err)
		}
	}
	var pending error
	if r.GetConfirmed() {
		_, pending = s.billing.ProcessTierUpgrade(ctx, r.GetTenantId(), r.GetUpgradeId())
	}
	row, err := purserdb.New(s.db).GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: r.GetTenantId(), ID: r.GetUpgradeId()})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant tier upgrade not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "read upgrade recovery: %v", err)
	}
	detail := "Recovery inspects the existing purchase. It cancels an unapplied request or resumes reconciliation of an applied purchase; it never starts another purchase. Stripe uses its actual replacement anchor, which may require metering repair before local pricing and collection can finish."
	if r.GetConfirmed() && row.State == "completed" {
		detail = "The provider purchase and local pricing are reconciled; invoice collection is recorded where required."
	} else if r.GetConfirmed() && row.State == "cancelled" {
		detail = "The purchase is cancelled. Any required provider refund is tracked separately until bank settlement."
	}
	if pending != nil {
		detail = fmt.Sprintf("Recovery is durably queued and remains pending: %v", pending)
	}
	return &purserpb.AdminRecoverTierUpgradeResponse{UpgradeId: row.ID, State: row.State, Provider: row.Provider, TierId: row.TierID, RequestedBy: row.RecoveryActorID.String, Reason: row.RecoveryReason.String, Detail: detail}, nil
}
