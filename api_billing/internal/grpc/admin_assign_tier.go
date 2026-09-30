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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// operatorTierBillingModel returns the billing model an operator-assigned tier
// runs under. Level-0 and default-prepaid tiers are balance-funded
// pay-as-you-go and run prepaid; every other tier is invoiced and runs
// postpaid. This is the same split PromoteToPaid and ChangeBillingTier enforce,
// and the catalog has no tier that supports both models. requested, when set,
// must match.
func operatorTierBillingModel(tierName string, tierLevel int32, isDefaultPrepaid bool, requested string) (string, error) {
	model := "postpaid"
	if isDefaultPrepaid || tierLevel < 1 {
		model = "prepaid"
	}
	if requested != "" && requested != model {
		return "", status.Errorf(codes.FailedPrecondition,
			"billing tier %q (level %d) runs %s; it cannot be assigned with billing model %s", tierName, tierLevel, model, requested)
	}
	return model, nil
}

// AdminAssignTier puts a tenant on a tier and billing model as an operator
// decision. Unlike ChangeBillingTier it applies downgrades immediately, moves
// between prepaid and postpaid in either direction, and does not require a
// payment provider or billing profile. A tenant with no subscription gets one.
// Cluster access and the tenant cache follow the same post-commit path as the
// self-serve tier changes; the subscription trigger queues the media-authority
// refresh inside the transaction.
func (s *PurserServer) AdminAssignTier(ctx context.Context, req *purserpb.AdminAssignTierRequest) (*purserpb.AdminAssignTierResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	tierName := strings.TrimSpace(req.GetTierName())
	reason := strings.TrimSpace(req.GetReason())
	requestedModel := strings.ToLower(strings.TrimSpace(req.GetBillingModel()))
	switch {
	case tenantID == "":
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	case tierName == "":
		return nil, status.Error(codes.InvalidArgument, "tier_name is required")
	case reason == "":
		return nil, status.Error(codes.InvalidArgument, "reason is required; it is recorded on the subscription audit event")
	case requestedModel != "" && requestedModel != "prepaid" && requestedModel != "postpaid":
		return nil, status.Errorf(codes.InvalidArgument, `billing_model must be "prepaid" or "postpaid", got %q`, req.GetBillingModel())
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	userID := middleware.GetUserID(ctx)

	var resp *purserpb.AdminAssignTierResponse
	var creditReturns []handlers.InvoiceCreditReturn
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		resp, creditReturns = nil, nil
		queries := purserdb.New(tx)
		tier, err := queries.GetTierByNameForOperatorAssignment(ctx, tierName)
		if errors.Is(err, sql.ErrNoRows) {
			return status.Errorf(codes.NotFound, "billing tier %q does not exist", tierName)
		}
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "load billing tier: %v", err)
		}
		if !tier.IsActive {
			return status.Errorf(codes.FailedPrecondition, "billing tier %q is inactive", tierName)
		}
		model, err := operatorTierBillingModel(tier.TierName, tier.TierLevel, tier.IsDefaultPrepaid, requestedModel)
		if err != nil {
			return err
		}
		tierUUID, err := uuid.Parse(tier.ID)
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "billing tier %q has malformed id %q", tierName, tier.ID)
		}

		now := time.Now()
		created := false
		current, err := queries.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err = queries.EnsureDefaultTenantSubscription(ctx, purserdb.EnsureDefaultTenantSubscriptionParams{
				ID: uuid.New(), TenantID: tenantID, TierID: tierUUID, BillingModel: model,
				Now: sql.NullTime{Time: now, Valid: true},
			}); err != nil {
				return txStatusErrorf(err, codes.Internal, "create subscription: %v", err)
			}
			created = true
			current, err = queries.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
		}
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "load subscription: %v", err)
		}
		if current.Status == "cancelled" {
			return status.Error(codes.FailedPrecondition, "subscription is cancelled because the account was closed; a closed account cannot be assigned a tier")
		}

		resp = &purserpb.AdminAssignTierResponse{
			SubscriptionId: current.SubscriptionID, TierId: tier.ID, TierName: tier.TierName,
			TierLevel: tier.TierLevel, BillingModel: model,
		}
		newStatus := current.Status
		var changedFields []string
		if created {
			changedFields = []string{"tier_id", "billing_model"}
		} else {
			resp.PreviousTierName = current.TierName
			resp.PreviousBillingModel = current.BillingModel
			if current.TierID != tier.ID {
				changedFields = append(changedFields, "tier_id")
			}
			if current.BillingModel != model {
				changedFields = append(changedFields, "billing_model")
			}
			if current.BillingModel == "prepaid" && model == "postpaid" && current.Status != "active" {
				newStatus = "active"
				changedFields = append(changedFields, "status")
			}
			if current.HasPendingTier {
				changedFields = append(changedFields, "pending_tier_id")
			}
		}
		if len(changedFields) == 0 {
			return nil
		}
		resp.Changed = true

		if !created {
			if _, err = queries.AssignTenantSubscriptionTier(ctx, purserdb.AssignTenantSubscriptionTierParams{
				TierID: tier.ID, BillingModel: model, TenantID: tenantID,
			}); err != nil {
				return txStatusErrorf(err, codes.Internal, "assign subscription tier: %v", err)
			}
		}
		if model == "prepaid" {
			if _, err = queries.EnsureRuntimePrepaidBalance(ctx, purserdb.EnsureRuntimePrepaidBalanceParams{
				ID: uuid.New(), TenantID: tenantID, Currency: billing.LedgerCurrency,
				Now: sql.NullTime{Time: now, Valid: true},
			}); err != nil {
				return txStatusErrorf(err, codes.Internal, "create prepaid balance: %v", err)
			}
			if !created && current.BillingModel != "prepaid" {
				if creditReturns, err = handlers.ReturnOpenInvoicePrepaidCreditTx(ctx, tx, tenantID); err != nil {
					return txStatusErrorf(err, codes.Internal, "return open invoice credit to prepaid balance: %v", err)
				}
			}
		} else {
			periodStart, periodEnd, periodErr := resolveBillingPeriod(ctx, tx, tenantID, current.BillingPeriodStart, current.BillingPeriodEnd, now)
			if periodErr != nil {
				return txStatusErrorf(periodErr, codes.Internal, "resolve billing period: %v", periodErr)
			}
			if err = queries.BackfillTenantBillingPeriod(ctx, purserdb.BackfillTenantBillingPeriodParams{
				PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
				PeriodEnd:   sql.NullTime{Time: periodEnd, Valid: true}, TenantID: tenantID,
			}); err != nil {
				return txStatusErrorf(err, codes.Internal, "backfill billing period: %v", err)
			}
		}

		updated, err := billingevents.New(tenantID, current.SubscriptionID, &internalv1.SubscriptionUpdated{
			SubscriptionId: current.SubscriptionID, TierId: tier.ID, Status: newStatus,
			ChangedFields: changedFields, Reason: reason,
		}, s.domainActor(ctx))
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "build subscription_updated: %v", err)
		}
		if _, err = s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, userID, "subscription", current.SubscriptionID, &ipcpb.BillingEvent{
			SubscriptionId: current.SubscriptionID, Status: newStatus,
		}, updated); err != nil {
			return txStatusErrorf(err, codes.Internal, "enqueue subscription_updated: %v", err)
		}
		return nil
	})
	switch {
	case err == nil:
	case stage == txStageBegin:
		return nil, status.Errorf(codes.Internal, "begin tier assignment: %v", err)
	case stage == txStageCommit:
		return nil, status.Errorf(codes.Internal, "commit tier assignment: %v", err)
	default:
		return nil, err
	}
	for _, credit := range creditReturns {
		s.logger.WithFields(logging.Fields{
			"tenant_id":      tenantID,
			"invoice_id":     credit.InvoiceID,
			"billing_period": credit.PeriodStart.Format("2006-01"),
			"returned_cents": credit.ReturnedCents,
			"balance_cents":  credit.BalanceCents,
			"operator":       userID,
		}).Info("Returned open invoice credit to prepaid balance on move to prepaid")
	}

	eligibleClusters, primaryCluster, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID)
	if clusterErr != nil {
		s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Error("reconcile cluster access after operator tier assignment")
		return nil, status.Errorf(codes.Internal, "tier assigned but failed to reconcile cluster access: %v", clusterErr)
	}
	if invErr := s.invalidateTenantCache(ctx, tenantID, "tier_changed"); invErr != nil {
		s.logger.WithError(invErr).WithField("tenant_id", tenantID).Error("invalidate tenant cache after operator tier assignment")
		return nil, status.Errorf(codes.Internal, "tier assigned but failed to invalidate tenant cache: %v", invErr)
	}
	resp.EligibleClusterIds = eligibleClusters
	resp.PrimaryClusterId = primaryCluster

	s.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"operator":        userID,
		"tier":            resp.GetTierName(),
		"billing_model":   resp.GetBillingModel(),
		"previous_tier":   resp.GetPreviousTierName(),
		"previous_model":  resp.GetPreviousBillingModel(),
		"changed":         resp.GetChanged(),
		"assign_reason":   reason,
		"primary_cluster": primaryCluster,
	}).Info("Operator assigned billing tier")
	return resp, nil
}
