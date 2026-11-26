package resolvers

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func (r *Resolver) platformBillingWrite(ctx context.Context, action, tenantID, reason string) error {
	if err := r.RequirePlatformOperator(ctx); err != nil {
		return err
	}
	if middleware.IsDemoMode(ctx) {
		return middleware.InvalidInput("billing changes are unavailable in demo mode")
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return middleware.InvalidInput("tenantId must be a UUID")
	}
	if strings.TrimSpace(reason) == "" {
		return middleware.InvalidInput("an audit reason is required")
	}
	r.Logger.WithFields(logging.Fields{"operator_user_id": ctxkeys.GetUserID(ctx), "audit": "platform_billing_write", "action": action, "target_tenant_id": tenantID, "reason": reason}).Info("Operator billing request")
	return nil
}

func (r *Resolver) DoPlatformAdjustBalance(ctx context.Context, tenantID string, amountCents int, reason, referenceID string) (*model.BalanceTransaction, error) {
	if err := r.platformBillingWrite(ctx, "adjust_balance", tenantID, reason); err != nil {
		return nil, err
	}
	if amountCents == 0 {
		return nil, middleware.InvalidInput("amount must be non-zero")
	}
	if _, err := uuid.Parse(referenceID); err != nil {
		return nil, middleware.InvalidInput("referenceId must be a UUID; reuse it for retries")
	}
	kind := "operator_adjustment"
	description := "operator credit: " + strings.TrimSpace(reason)
	if amountCents < 0 {
		description = "operator debit: " + strings.TrimSpace(reason)
	}
	tx, err := r.Clients.Purser.AdjustBalance(ctx, tenantID, int64(amountCents), description, &referenceID, &kind)
	if err != nil {
		return nil, err
	}
	if tx == nil || tx.AmountCents != int64(amountCents) {
		return nil, fmt.Errorf("balance receipt does not match the request; inspect the reference before retrying")
	}
	return &model.BalanceTransaction{ID: tx.Id, TenantID: tx.TenantId, AmountCents: int(tx.AmountCents), BalanceAfterCents: int(tx.BalanceAfterCents), TransactionType: tx.TransactionType, Description: &tx.Description, ReferenceID: tx.ReferenceId, ReferenceType: tx.ReferenceType, CreatedAt: tx.CreatedAt.AsTime()}, nil
}

func (r *Resolver) DoPlatformAssignBillingTier(ctx context.Context, tenantID, tierName, reason string) (*model.PlatformBillingAssignment, error) {
	if err := r.platformBillingWrite(ctx, "assign_tier", tenantID, reason); err != nil {
		return nil, err
	}
	if strings.TrimSpace(tierName) == "" {
		return nil, middleware.InvalidInput("tierName is required")
	}
	out, err := r.Clients.Purser.AdminAssignTier(ctx, &purserpb.AdminAssignTierRequest{TenantId: tenantID, TierName: strings.TrimSpace(tierName), Reason: strings.TrimSpace(reason)})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("missing assignment receipt; reload billing before retrying")
	}
	return &model.PlatformBillingAssignment{SubscriptionID: out.SubscriptionId, TierName: out.TierName, BillingModel: out.BillingModel, Changed: out.Changed}, nil
}

func (r *Resolver) DoPlatformRecoverCheckout(ctx context.Context, tenantID, reservationID, reason string, confirmed bool) (*model.PlatformCheckoutRecovery, error) {
	if err := r.platformBillingWrite(ctx, "recover_checkout", tenantID, reason); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(reservationID); err != nil {
		return nil, middleware.InvalidInput("reservationId must be a UUID")
	}
	out, err := r.Clients.Purser.AdminRecoverCheckout(ctx, &purserpb.AdminRecoverCheckoutRequest{TenantId: tenantID, ReservationId: reservationID, Reason: strings.TrimSpace(reason), Confirmed: confirmed})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("missing checkout recovery receipt; inspect recovery before retrying")
	}
	return &model.PlatformCheckoutRecovery{Provider: out.Provider, Status: out.Status, AmountCents: int(out.AmountCents), Currency: out.Currency, AmountKnown: out.AmountKnown, Outcome: out.Outcome, Detail: out.Detail, AuditID: out.AuditId}, nil
}

func (r *Resolver) DoPlatformTenantSubscription(ctx context.Context, tenantID string) (*purserpb.TenantSubscription, error) {
	if err := r.platformGate(ctx, "platform.tenant.billing.subscription", tenantID); err != nil {
		return nil, err
	}
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateBillingStatus().Subscription, nil
	}
	out, err := r.Clients.Purser.GetSubscription(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return out.GetSubscription(), nil
}

func (r *Resolver) DoPlatformPreviewBillingTier(ctx context.Context, tenant, tier, reason string) (*model.PlatformBillingAssignmentQuote, error) {
	if err := r.platformBillingWrite(ctx, "preview_tier", tenant, reason); err != nil {
		return nil, err
	}
	out, err := r.Clients.Purser.PreviewAdminAssignTier(ctx, &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: strings.TrimSpace(tier), Reason: strings.TrimSpace(reason)})
	if err != nil {
		return nil, err
	}
	if out == nil || out.QuotedAt == nil || out.ExpiresAt == nil {
		return nil, fmt.Errorf("missing assignment quote")
	}
	return &model.PlatformBillingAssignmentQuote{QuotedAt: out.QuotedAt.AsTime(), ExpiresAt: out.ExpiresAt.AsTime(), Fingerprint: out.Fingerprint, EffectsJSON: out.EffectsJson}, nil
}
func (r *Resolver) DoPlatformAssignBillingTierWithReview(ctx context.Context, tenant, tier, reason string, at time.Time, fingerprint string) (*model.PlatformBillingAssignment, error) {
	if err := r.platformBillingWrite(ctx, "assign_reviewed_tier", tenant, reason); err != nil {
		return nil, err
	}
	out, err := r.Clients.Purser.AdminAssignTierWithReview(ctx, &purserpb.AdminAssignTierReviewedRequest{Assignment: &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: strings.TrimSpace(tier), Reason: strings.TrimSpace(reason)}, QuotedAt: timestamppb.New(at), ExpectedFingerprint: fingerprint})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("missing assignment receipt; reload billing before retrying")
	}
	return &model.PlatformBillingAssignment{SubscriptionID: out.SubscriptionId, TierName: out.TierName, BillingModel: out.BillingModel, Changed: out.Changed}, nil
}
