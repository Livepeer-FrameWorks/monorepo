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
// payment provider or billing profile. A move between models closes the
// current phase at the switch with its billing document: a prepaid statement,
// or a finalized postpaid invoice that is then collected. A tenant with no
// subscription gets one.
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
	// A switch that closes a phase is rated before its transaction; a
	// subscription or usage that changed in between rates it again.
	// After the last attempt the caller gets codes.Aborted.
	for attempt := 1; ; attempt++ {
		previous, err := loadGrantForMutation(ctx, s.db, tenantID)
		if err != nil {
			return nil, err
		}
		if previous != nil && previous.Active {
			current, readErr := purserdb.New(s.db).GetSubscriptionTierForGrant(ctx, tenantID)
			if readErr != nil {
				return nil, status.Errorf(codes.Internal, "load current subscription tier: %v", readErr)
			}
			if current.TierName == tierName && (requestedModel == "" || requestedModel == current.BillingModel) {
				return s.adminAssignTierOnce(ctx, tenantID, tierName, requestedModel, reason, nil)
			}
		}
		var grant *operatorGrantMutation
		if previous != nil {
			grant = &operatorGrantMutation{previous: previous, reason: reason, clearFallback: true}
			if previous.ExpiresAt.Valid && !previous.ExpiresAt.Time.After(time.Now()) {
				grant.at = previous.ExpiresAt.Time
			} else {
				grant.change = &operatorGrantChange{basePrice: previous.BasePrice, waiveUsage: previous.WaiveUsage, collection: previous.Collection, expiresAt: previous.ExpiresAt, reason: previous.Reason}
			}
		}
		resp, err := s.adminAssignTierOnce(ctx, tenantID, tierName, requestedModel, reason, grant)
		if errors.Is(err, errPhaseChangedWhileRated) {
			if attempt < phaseCloseAttempts {
				continue
			}
			return nil, status.Error(codes.Aborted, "billing phase changed while assigning the tier; retry")
		}
		return resp, err
	}
}

// errSubscriptionCancelled lets grant ending recheck cancellation under its cleanup lock.
var errSubscriptionCancelled = status.Error(codes.FailedPrecondition, "subscription is cancelled; end any outstanding grant with grant revoke, then start provider checkout")

// phaseCloseAttempts bounds how often a tier assignment rates a closing
// document again because the subscription or its usage changed meanwhile.
const phaseCloseAttempts = 3

// errPhaseChangedWhileRated means the document that closes the current
// billing phase was rated for a subscription or usage that changed before
// the switch committed.
var errPhaseChangedWhileRated = status.Error(codes.Aborted, "the billing phase changed while its closing document was prepared")

func (s *PurserServer) adminAssignTierOnce(ctx context.Context, tenantID, tierName, requestedModel, reason string, grant *operatorGrantMutation) (*purserpb.AdminAssignTierResponse, error) {
	userID := middleware.GetUserID(ctx)

	// A move between prepaid and postpaid closes the current phase: from
	// prepaid with its statement, from postpaid with its invoice. The closing
	// document is rated here, outside the transaction.
	var phase *handlers.PrepaidPhaseClose
	var prepareErr error
	if grant == nil {
		phase, prepareErr = s.preparePrepaidPhaseCloseForTier(ctx, tenantID, tierName, requestedModel)
		if prepareErr != nil {
			return nil, prepareErr
		}
	}

	var postpaidPhase *handlers.PostpaidPhaseClose
	if grant == nil {
		postpaidPhase, prepareErr = s.preparePostpaidPhaseCloseForTier(ctx, tenantID, tierName, requestedModel)
		if prepareErr != nil {
			return nil, prepareErr
		}
	}

	if grant != nil {
		target, err := purserdb.New(s.db).GetTierByNameForOperatorAssignment(ctx, tierName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "billing tier %q does not exist", tierName)
		}
		if err != nil {
			return nil, status.Errorf(codes.Internal, "load billing tier: %v", err)
		}
		model, err := operatorTierBillingModel(target.TierName, target.TierLevel, target.IsDefaultPrepaid, requestedModel)
		if grant.keepTier {
			model, err = requestedModel, nil
		}
		if err != nil {
			return nil, err
		}
		current, loadErr := purserdb.New(s.db).GetSubscriptionTierForGrant(ctx, tenantID)
		if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "load subscription: %v", loadErr)
		}
		grant.expectedTierID, grant.expectedModel = current.TierID, current.BillingModel
		grant.pricingBoundary = loadErr == nil && (current.TierID != target.ID || current.BillingModel != model || grant.changesPrices())
		if grant.pricingBoundary {
			phase, prepareErr = handlers.PreparePrepaidGrantBoundary(ctx, s.db, s.logger, s.billing, tenantID, grant.at)
			if prepareErr != nil {
				return nil, status.Errorf(codes.Internal, "prepare grant billing boundary: %v", prepareErr)
			}
			if phase != nil {
				grant.at = phase.BoundaryAt()
			}
			postpaidPhase, prepareErr = handlers.PreparePostpaidGrantBoundary(ctx, s.db, s.logger, s.billing, tenantID, grant.at, model == "prepaid")
			if prepareErr != nil {
				return nil, status.Errorf(codes.Internal, "prepare grant billing boundary: %v", prepareErr)
			}
		}
	}

	if grant != nil && postpaidPhase != nil {
		grant.at = postpaidPhase.BoundaryAt()
	}
	var resp *purserpb.AdminAssignTierResponse
	var creditReturns []handlers.InvoiceCreditReturn
	phaseClosed, postpaidPhaseClosed := false, false
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		phaseClosed, postpaidPhaseClosed = false, false
		resp, creditReturns = nil, nil
		queries := purserdb.New(tx)
		tier, err := queries.GetTierByNameForOperatorAssignment(ctx, tierName)
		if errors.Is(err, sql.ErrNoRows) {
			return status.Errorf(codes.NotFound, "billing tier %q does not exist", tierName)
		}
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "load billing tier: %v", err)
		}
		if !tier.IsActive && (grant == nil || !grant.keepTier) {
			return status.Errorf(codes.FailedPrecondition, "billing tier %q is inactive", tierName)
		}
		model, err := operatorTierBillingModel(tier.TierName, tier.TierLevel, tier.IsDefaultPrepaid, requestedModel)
		if grant != nil && grant.keepTier {
			model, err = requestedModel, nil
		}
		if err != nil {
			return err
		}
		tierUUID, err := uuid.Parse(tier.ID)
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "billing tier %q has malformed id %q", tierName, tier.ID)
		}

		now := time.Now()
		created := false
		var current purserdb.LockTenantSubscriptionForOperatorAssignmentRow
		// Expiry skips a row another replica owns; operator commands wait for its lock.
		if grant != nil && grant.expiredOnly {
			var locked purserdb.LockTenantSubscriptionForGrantExpiryRow
			locked, err = queries.LockTenantSubscriptionForGrantExpiry(ctx, tenantID)
			current = purserdb.LockTenantSubscriptionForOperatorAssignmentRow(locked)
			if errors.Is(err, sql.ErrNoRows) {
				return errOperatorGrantBusy
			}
		} else {
			current, err = queries.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
		}
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
		if grant != nil && grant.keepTier && (current.TierID != tier.ID || current.BillingModel != model) {
			return errPhaseChangedWhileRated
		}
		accountClosed, closedErr := queries.IsTenantBillingClosed(ctx, tenantID)
		if closedErr != nil {
			return txStatusErrorf(closedErr, codes.Internal, "read account closure: %v", closedErr)
		}
		if accountClosed && (grant == nil || grant.change != nil) {
			return status.Error(codes.FailedPrecondition, subscriptions.ErrTenantBillingClosed.Error())
		}
		if current.Status == "cancelled" {
			return errSubscriptionCancelled
		}
		if upgradeErr := subscriptions.CheckPendingTierUpgrade(ctx, tx, tenantID); upgradeErr != nil {
			if subscriptions.IsPricingRefusal(upgradeErr) {
				return status.Error(codes.FailedPrecondition, upgradeErr.Error())
			}
			return txStatusErrorf(upgradeErr, codes.Internal, "check tier upgrade fence: %v", upgradeErr)
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
			if current.HasPendingTier && (grant == nil || !grant.keepTier) {
				changedFields = append(changedFields, "pending_tier_id")
			}
		}
		if grant == nil && !created && (current.TierID != tier.ID || current.BillingModel != model) {
			if _, guardErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.ChangePricing); guardErr != nil {
				if subscriptions.IsPricingRefusal(guardErr) {
					return errPhaseChangedWhileRated
				}
				return txStatusErrorf(guardErr, codes.Internal, "check operator assignment grant: %v", guardErr)
			}
		}
		if grant != nil {
			if !created && (current.TierID != grant.expectedTierID || current.BillingModel != grant.expectedModel) {
				return errPhaseChangedWhileRated
			}
			if grantErr := grant.checkUnchanged(ctx, queries, tenantID); grantErr != nil {
				return grantErr
			}
			if !created && current.BillingModel == "postpaid" && grant.pricingBoundary {
				if postpaidPhaseClosed, err = commitPostpaidPhaseClose(ctx, tx, postpaidPhase); err != nil {
					return err
				}
			}
		}
		if !created && current.BillingModel == "prepaid" && (grant == nil && model == "postpaid" || grant != nil && grant.pricingBoundary) {
			if phaseClosed, err = commitPrepaidPhaseClose(ctx, tx, phase); err != nil {
				return err
			}
		}
		if grant == nil && !created && current.BillingModel == "postpaid" && model == "prepaid" {
			if postpaidPhaseClosed, err = commitPostpaidPhaseClose(ctx, tx, postpaidPhase); err != nil {
				return err
			}
		}
		if grant != nil {
			if grantErr := s.writeOperatorGrantMutationTx(ctx, tx, current, tenantID, tier.ID, grant); grantErr != nil {
				return grantErr
			}
		}
		if grant == nil || grant.clearFallback {
			pricing, lockErr := queries.LockTenantSubscriptionForProviderChange(ctx, tenantID)
			if lockErr != nil {
				return txStatusErrorf(lockErr, codes.Internal, "read fallback stop fence: %v", lockErr)
			}
			if pricing.FreeFallbackProviderStopping {
				return status.Error(codes.FailedPrecondition, subscriptions.ErrPendingFreeFallback.Error())
			}
			cleared, clearErr := queries.ClearSubscriptionFreeFallback(ctx, tenantID)
			if clearErr != nil {
				return txStatusErrorf(clearErr, codes.Internal, "cancel scheduled fallback: %v", clearErr)
			}
			if cleared > 0 {
				changedFields = append(changedFields, "free_fallback")
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
		} else if !phaseClosed {
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
	if grant != nil && !grant.mutated && !resp.GetChanged() {
		return resp, nil
	}
	if phaseClosed {
		phase.Log()
	}
	if postpaidPhaseClosed {
		postpaidPhase.AfterCommit(ctx)
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
		if grant == nil {
			return nil, status.Errorf(codes.Internal, "tier assigned but failed to reconcile cluster access: %v", clusterErr)
		}
	}
	if invErr := s.invalidateTenantCache(ctx, tenantID, "tier_changed"); invErr != nil {
		s.logger.WithError(invErr).WithField("tenant_id", tenantID).Error("invalidate tenant cache after operator tier assignment")
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

// preparePrepaidPhaseCloseForTier rates the closing statement of the tenant's
// prepaid phase when assigning tierName moves it to postpaid. A missing tier
// or a model the tier does not run is refused by the assignment's
// transaction, so neither prepares anything here.
func (s *PurserServer) preparePrepaidPhaseCloseForTier(ctx context.Context, tenantID, tierName, requestedModel string) (*handlers.PrepaidPhaseClose, error) {
	tier, err := purserdb.New(s.db).GetTierByNameForOperatorAssignment(ctx, tierName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load billing tier: %v", err)
	}
	if model, modelErr := operatorTierBillingModel(tier.TierName, tier.TierLevel, tier.IsDefaultPrepaid, requestedModel); modelErr == nil && model == "postpaid" {
		return s.preparePrepaidPhaseClose(ctx, tenantID)
	}
	return nil, nil
}

// preparePrepaidPhaseClose rates the closing statement of the tenant's prepaid
// phase for a switch to postpaid now. It is nil for a tenant that is not
// prepaid.
func (s *PurserServer) preparePrepaidPhaseClose(ctx context.Context, tenantID string) (*handlers.PrepaidPhaseClose, error) {
	phase, err := handlers.PreparePrepaidPhaseClose(ctx, s.db, s.logger, s.billing, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prepare the statement that closes the prepaid period: %v", err)
	}
	return phase, nil
}

// commitPrepaidPhaseClose writes the prepared closing statement inside the
// transaction that switches the tenant from prepaid to postpaid, and starts
// the postpaid period at the switch. It reports whether a prepaid phase was
// closed.
func commitPrepaidPhaseClose(ctx context.Context, tx *sql.Tx, phase *handlers.PrepaidPhaseClose) (bool, error) {
	if phase == nil {
		return false, txStatusErrorf(errPhaseChangedWhileRated, codes.Aborted, "the subscription became prepaid while the switch was prepared; retry")
	}
	closed, err := phase.CommitTx(ctx, tx)
	if errors.Is(err, handlers.ErrPrepaidPhaseChanged) {
		return false, txStatusErrorf(errPhaseChangedWhileRated, codes.Aborted, "%v; retry", err)
	}
	if err != nil {
		return false, txStatusErrorf(err, codes.Internal, "close the prepaid period: %v", err)
	}
	return closed, nil
}

// preparePostpaidPhaseCloseForTier rates the invoice that closes the tenant's
// postpaid phase when assigning tierName moves it to prepaid. A missing tier
// or a model the tier does not run is refused by the assignment's
// transaction, so neither prepares anything here.
func (s *PurserServer) preparePostpaidPhaseCloseForTier(ctx context.Context, tenantID, tierName, requestedModel string) (*handlers.PostpaidPhaseClose, error) {
	tier, err := purserdb.New(s.db).GetTierByNameForOperatorAssignment(ctx, tierName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load billing tier: %v", err)
	}
	if model, modelErr := operatorTierBillingModel(tier.TierName, tier.TierLevel, tier.IsDefaultPrepaid, requestedModel); modelErr == nil && model == "prepaid" {
		phase, prepareErr := handlers.PreparePostpaidPhaseClose(ctx, s.db, s.logger, s.billing, tenantID)
		if prepareErr != nil {
			return nil, status.Errorf(codes.Internal, "prepare the invoice that closes the postpaid period: %v", prepareErr)
		}
		return phase, nil
	}
	return nil, nil
}

// commitPostpaidPhaseClose finalizes the prepared closing invoice inside the
// transaction that switches the tenant from postpaid to prepaid, and starts
// the prepaid period at the switch. It reports whether a postpaid phase was
// closed.
func commitPostpaidPhaseClose(ctx context.Context, tx *sql.Tx, phase *handlers.PostpaidPhaseClose) (bool, error) {
	if phase == nil {
		return false, txStatusErrorf(errPhaseChangedWhileRated, codes.Aborted, "the subscription became postpaid while the switch was prepared; retry")
	}
	closed, err := phase.CommitTx(ctx, tx)
	if errors.Is(err, handlers.ErrPostpaidPhaseChanged) {
		return false, txStatusErrorf(errPhaseChangedWhileRated, codes.Aborted, "%v; retry", err)
	}
	if errors.Is(err, handlers.ErrBaseFeePaymentPending) {
		return false, txStatusErrorf(err, codes.FailedPrecondition, "close the postpaid period: %v; retry once that payment has resolved", err)
	}
	if err != nil {
		return false, txStatusErrorf(err, codes.Internal, "close the postpaid period: %v", err)
	}
	return closed, nil
}
