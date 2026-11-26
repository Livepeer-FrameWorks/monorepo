package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EnablePrepaidUsage records the customer's intent before reconciling funding.
// It never replaces a paid subscription or an operator's pricing arrangement.
func (s *PurserServer) EnablePrepaidUsage(ctx context.Context, req *purserpb.EnablePrepaidUsageRequest) (*purserpb.EnablePrepaidUsageResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if !middleware.IsServiceCall(ctx) && !ctxkeys.IsPlatformOperator(ctx) && ctxkeys.GetTenantID(ctx) != tenantID {
		return nil, status.Error(codes.PermissionDenied, "subscription tenant does not match authenticated tenant")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "funded usage activation is unavailable")
	}
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		current, err := q.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "subscription not found")
		}
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "read funding arrangement: %v", err)
		}
		if current.TierName != "free" && current.BillingModel != "prepaid" {
			return billingRefusal("PAID_PLAN_ACTIVE", "Your monthly plan stays active. Change that arrangement before enabling PAYG.")
		}
		if current.Status != "active" {
			return billingRefusal("ACCOUNT_NOT_ACTIVE", "This account cannot enable PAYG in its current state. Resolve its billing status first.")
		}
		if _, err = subscriptions.LockChange(ctx, tx, tenantID, subscriptions.ChangePricing); err != nil {
			if subscriptions.IsPricingRefusal(err) {
				return tenantPricingRefusal(err)
			}
			return txStatusErrorf(err, codes.Internal, "guard funded usage preference: %v", err)
		}
		if err = subscriptions.CheckGrantCheckout(ctx, tx, tenantID); err != nil {
			if errors.Is(err, subscriptions.ErrPendingCheckout) || subscriptions.IsPricingRefusal(err) {
				return tenantPricingRefusal(err)
			}
			return txStatusErrorf(err, codes.Internal, "guard pending purchase: %v", err)
		}
		changed, err := q.SetAutomaticPrepaidUsage(ctx, purserdb.SetAutomaticPrepaidUsageParams{TenantID: tenantID, Enabled: true})
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "save funded usage preference: %v", err)
		}
		if changed == 0 {
			return nil
		}
		ev, err := billingevents.New(tenantID, current.SubscriptionID, &internalv1.SubscriptionUpdated{SubscriptionId: current.SubscriptionID, TierId: current.TierID, Status: current.Status, ChangedFields: []string{"automatic_prepaid_usage"}, Reason: "Customer enabled balance-funded PAYG usage"}, s.domainActor(ctx))
		if err != nil {
			return err
		}
		_, err = s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, middleware.GetUserID(ctx), "subscription", current.SubscriptionID, &ipcpb.BillingEvent{SubscriptionId: current.SubscriptionID, Status: current.Status}, ev)
		return err
	})
	if err != nil {
		if stage == txStageBegin || stage == txStageCommit {
			return nil, status.Error(codes.Internal, "could not save funded usage preference")
		}
		return nil, err
	}
	activated, activationErr := s.billing.ActivateFundedFree(ctx, tenantID)
	if activationErr != nil {
		err = activationErr
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("PAYG preference saved; activation awaits reconciliation")
		return nil, billingRefusal("PREPAID_ACTIVATION_PENDING", "Your PAYG choice is saved. Activation is still being reconciled; refresh billing before starting new paid work.")
	}
	if activated && s.commodoreClient != nil {
		if _, cacheErr := s.commodoreClient.InvalidateTenantCache(ctx, tenantID, "prepaid_usage_enabled"); cacheErr != nil {
			s.logger.WithError(cacheErr).WithField("tenant_id", tenantID).Warn("PAYG cache refresh awaits durable convergence")
		}
	}
	if err = s.billing.ConvergeTenantEntitlements(ctx, tenantID); err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("PAYG access convergence pending")
	}
	current, err := purserdb.New(s.db).GetCanonicalPromotedSubscription(ctx, purserdb.GetCanonicalPromotedSubscriptionParams{TenantID: tenantID, Currency: billing.LedgerCurrency})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "reload billing to inspect the saved PAYG choice")
	}
	if current.TierName == "free" && current.BalanceCents > 0 {
		return nil, billingRefusal("PREPAID_ACTIVATION_PENDING", "Your PAYG choice is saved. Activation is waiting for the current plan change to finish; refresh billing before starting new paid work.")
	}
	return &purserpb.EnablePrepaidUsageResponse{Activated: current.BillingModel == "prepaid", AwaitingFunds: current.BalanceCents <= 0, TierName: current.TierName, BillingModel: current.BillingModel}, nil
}

func (s *PurserServer) retainCurrentFreeFundingPreferenceTx(ctx context.Context, tx *sql.Tx, tenantID, tierID string) error {
	row, err := purserdb.New(tx).RetainCurrentFreeFundingPreference(ctx, purserdb.RetainCurrentFreeFundingPreferenceParams{TenantID: tenantID, TierID: tierID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return txStatusErrorf(err, codes.Internal, "retain free funding preference: %v", err)
	}
	ev, err := billingevents.New(tenantID, row.SubscriptionID, &internalv1.SubscriptionUpdated{SubscriptionId: row.SubscriptionID, TierId: tierID, Status: row.Status, ChangedFields: []string{"automatic_prepaid_usage"}, Reason: "Customer retained Free limits and wallet credit"}, s.domainActor(ctx))
	if err != nil {
		return err
	}
	_, err = s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, middleware.GetUserID(ctx), "subscription", row.SubscriptionID, &ipcpb.BillingEvent{SubscriptionId: row.SubscriptionID, Status: row.Status}, ev)
	return err
}
