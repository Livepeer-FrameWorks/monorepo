package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminRecoverTierDowngrade previews a downgrade past its effective time or,
// confirmed, records the operator's request and runs the tenant's period close
// and downgrade applier now. Every hold the applier enforces still applies; a
// manual_review invoice stays held until its cluster pricing is resolved.
func (s *PurserServer) AdminRecoverTierDowngrade(ctx context.Context, r *purserpb.AdminRecoverTierDowngradeRequest) (*purserpb.AdminRecoverTierDowngradeResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	reason := strings.TrimSpace(r.GetReason())
	if r.GetConfirmed() && (reason == "" || r.GetTargetTierId() == "") {
		return nil, status.Error(codes.InvalidArgument, "confirmation requires a reason and the preview's target_tier_id")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "downgrade recovery is unavailable")
	}
	preview, err := s.billing.TierDowngradeHold(ctx, r.GetTenantId())
	if err != nil {
		return nil, tierDowngradeRecoveryStatus(err)
	}
	if !r.GetConfirmed() {
		return &purserpb.AdminRecoverTierDowngradeResponse{Status: "held", Downgrade: heldTierDowngradeProto(preview.Hold), Detail: preview.Detail}, nil
	}
	if preview.Hold.TargetTierID != r.GetTargetTierId() {
		return nil, status.Error(codes.FailedPrecondition, "the downgrade target changed after preview; preview it again")
	}
	_, err = runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		locked, lockErr := subscriptions.LockChange(ctx, tx, r.GetTenantId(), subscriptions.PreservePricing)
		if lockErr != nil {
			return lockErr
		}
		return billingevents.NewAndEnqueue(ctx, tx, r.GetTenantId(), locked.Current.SubscriptionID, &internalv1.SubscriptionUpdated{
			SubscriptionId: locked.Current.SubscriptionID,
			ChangedFields:  []string{"tier_downgrade_recovery_requested"}, Reason: reason,
		}, s.domainActor(ctx))
	})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "record downgrade recovery: %v", err)
	}
	outcome, err := s.billing.RecoverHeldTierDowngrade(ctx, r.GetTenantId())
	if err != nil {
		return nil, tierDowngradeRecoveryStatus(err)
	}
	return &purserpb.AdminRecoverTierDowngradeResponse{Status: outcome.Status, Downgrade: heldTierDowngradeProto(outcome.Hold), Detail: outcome.Detail}, nil
}

func tierDowngradeRecoveryStatus(err error) error {
	if errors.Is(err, handlers.ErrNoTierDowngradeHold) {
		return status.Error(codes.NotFound, err.Error())
	}
	return status.Errorf(codes.Unavailable, "downgrade recovery: %v", err)
}

func heldTierDowngradeProto(hold purserdb.GetTierDowngradeHoldRow) *purserpb.HeldTierDowngrade {
	return &purserpb.HeldTierDowngrade{
		TenantId: hold.TenantID, TargetTierId: hold.TargetTierID, TargetTierName: hold.TargetTierName,
		EffectiveAt: timestamppb.New(hold.EffectiveAt), InvoiceId: hold.InvoiceID, InvoiceStatus: hold.InvoiceStatus,
	}
}

// listHeldTierDowngrades returns the downgrades PurserTierDowngradeHeld counts,
// optionally for one tenant.
func (s *PurserServer) listHeldTierDowngrades(ctx context.Context, tenantID string, limit int32) ([]*purserpb.HeldTierDowngrade, error) {
	q := purserdb.New(s.db)
	tenants, err := q.ListHeldTierDowngrades(ctx, purserdb.ListHeldTierDowngradesParams{
		GraceSeconds: int64(handlers.HeldDowngradeGrace / time.Second), TenantID: tenantID, RowLimit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*purserpb.HeldTierDowngrade, 0, len(tenants))
	for _, tenant := range tenants {
		hold, holdErr := q.GetTierDowngradeHold(ctx, tenant)
		if errors.Is(holdErr, sql.ErrNoRows) {
			continue
		}
		if holdErr != nil {
			return nil, holdErr
		}
		out = append(out, heldTierDowngradeProto(hold))
	}
	return out, nil
}
