package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/subscriptions"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// retainedProviderAssignment records the ended collector whose already-paid
// fee must remain excluded until its old invoicing phases are finalized.
type retainedProviderAssignment struct {
	proof          purserdb.GetRetainedProviderAssignmentRow
	phase          *handlers.PostpaidPhaseClose
	requireNoGrant bool
}

var errRetainedProviderGrant = status.Error(codes.FailedPrecondition, "end or explicitly reassign the outstanding billing grant before aborting its provider fallback")

func (r *retainedProviderAssignment) unchanged(ctx context.Context, q *purserdb.Queries, tenantID string) error {
	current, err := q.GetRetainedProviderAssignment(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return errPhaseChangedWhileRated
	}
	if err != nil {
		return txStatusErrorf(err, codes.Internal, "read retained provider cancellation: %v", err)
	}
	if current.StripeSubscriptionID != r.proof.StripeSubscriptionID || current.MollieSubscriptionID != r.proof.MollieSubscriptionID ||
		current.FreeFallbackProviderEndedAt != r.proof.FreeFallbackProviderEndedAt || current.FreeFallbackAt != r.proof.FreeFallbackAt ||
		current.StripeCurrentPeriodEnd != r.proof.StripeCurrentPeriodEnd {
		return errPhaseChangedWhileRated
	}
	if r.requireNoGrant {
		_, grantErr := q.GetSubscriptionOperatorGrant(ctx, tenantID)
		if grantErr == nil {
			return errRetainedProviderGrant
		}
		if !errors.Is(grantErr, sql.ErrNoRows) {
			return txStatusErrorf(grantErr, codes.Internal, "read retained provider billing grant: %v", grantErr)
		}
	}
	return nil
}

// prepareRetainedProviderAssignment closes an explicit fallback abort before
// detachment. Ordinary grants first determine whether their pricing changes.
func (s *PurserServer) prepareRetainedProviderAssignment(ctx context.Context, tenantID, tierName, requestedModel string, grant *operatorGrantMutation) (*retainedProviderAssignment, error) {
	if grant != nil && !grant.clearFallback {
		return nil, nil
	}
	return s.prepareRetainedProviderChange(ctx, tenantID, tierName, requestedModel, grant)
}

// prepareRetainedProviderChange catches up complete stored months without
// undoing cancellation. The final partial close commits with the pricing change
// or collector detachment; all monthly commits retain the original grant.
func (s *PurserServer) prepareRetainedProviderChange(ctx context.Context, tenantID, tierName, requestedModel string, grant *operatorGrantMutation) (*retainedProviderAssignment, error) {
	q := purserdb.New(s.db)
	proof, err := q.GetRetainedProviderAssignment(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read retained provider billing phase: %v", err)
	}
	if !proof.FreeFallbackAt.Valid {
		return nil, status.Error(codes.FailedPrecondition, "retained provider cancellation has no recorded fallback deadline; reconcile its cancellation before assigning a tier")
	}
	paidUntil := proof.FreeFallbackAt.Time
	if proof.StripeCurrentPeriodEnd.Valid && proof.StripeCurrentPeriodEnd.Time.After(paidUntil) {
		paidUntil = proof.StripeCurrentPeriodEnd.Time
	}
	if paidUntil.After(proof.DatabaseNow) {
		return nil, nil
	}
	if grant == nil {
		_, grantErr := q.GetSubscriptionOperatorGrant(ctx, tenantID)
		if grantErr == nil {
			return nil, errRetainedProviderGrant
		}
		if !errors.Is(grantErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "read retained provider billing grant: %v", grantErr)
		}
	}
	tier, err := q.GetTierByNameForOperatorAssignment(ctx, tierName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read assignment tier: %v", err)
	}
	model, err := operatorTierBillingModel(tier.TierName, tier.TierLevel, tier.IsDefaultPrepaid, requestedModel)
	if grant != nil && grant.keepTier {
		model, err = requestedModel, nil
	}
	if err != nil {
		return nil, err
	}
	if !tier.IsActive && (grant == nil || !grant.keepTier) {
		return nil, status.Error(codes.FailedPrecondition, "the assignment tier is inactive")
	}
	cutoff := proof.DatabaseNow.UTC().Truncate(time.Microsecond)
	if grant != nil {
		if grant.at.IsZero() {
			grant.at = cutoff
		} else {
			cutoff = grant.at
		}
	}
	prepared := &retainedProviderAssignment{proof: proof, requireNoGrant: grant == nil}
	for {
		closing, final, prepareErr := handlers.PrepareRetainedProviderBoundary(ctx, s.db, s.logger, s.billing, tenantID, cutoff, model == "prepaid", grant != nil)
		if prepareErr != nil {
			return nil, billingBoundaryStatus("prepare ended provider billing period", prepareErr)
		}
		if final {
			prepared.phase = closing
			return prepared, nil
		}
		if err := s.commitRetainedProviderMonth(ctx, tenantID, grant, prepared, closing); err != nil {
			return nil, err
		}
	}
}

// commitRetainedProviderMonth advances one complete month without clearing the
// cancellation. A later metering failure leaves this ordinary billing progress
// committed, with the retained native identities still excluding the paid fee.
func (s *PurserServer) commitRetainedProviderMonth(ctx context.Context, tenantID string, grant *operatorGrantMutation, prepared *retainedProviderAssignment, closing *handlers.PostpaidPhaseClose) error {
	closed := false
	_, commitErr := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		locked, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if lockErr != nil {
			return txStatusErrorf(lockErr, codes.Internal, "lock retained provider period: %v", lockErr)
		}
		if locked.AccountClosed {
			return status.Error(codes.FailedPrecondition, subscriptions.ErrTenantBillingClosed.Error())
		}
		if locked.Current.FreeFallbackProviderStopping {
			return status.Error(codes.FailedPrecondition, subscriptions.ErrPendingFreeFallback.Error())
		}
		if upgradeErr := subscriptions.CheckPendingTierUpgrade(ctx, tx, tenantID); upgradeErr != nil {
			if subscriptions.IsPricingRefusal(upgradeErr) {
				return status.Error(codes.FailedPrecondition, upgradeErr.Error())
			}
			return txStatusErrorf(upgradeErr, codes.Internal, "check retained provider upgrade: %v", upgradeErr)
		}
		queries := purserdb.New(tx)
		if proofErr := prepared.unchanged(ctx, queries, tenantID); proofErr != nil {
			return proofErr
		}
		if grant != nil {
			if grantErr := grant.checkUnchanged(ctx, queries, tenantID); grantErr != nil {
				return grantErr
			}
		}
		var closeErr error
		closed, closeErr = commitPostpaidPhaseClose(ctx, tx, closing)
		return closeErr
	})
	commitErr = handlers.RecordBillingPhaseTaxReview(ctx, commitErr)
	if commitErr != nil {
		return commitErr
	}
	if !closed {
		return errPhaseChangedWhileRated
	}
	closing.AfterCommit(ctx)
	return nil
}
