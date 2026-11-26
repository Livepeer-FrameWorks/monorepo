package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

// ActivateFundedFree starts canonical prepaid pricing for a funded Free account.
// The Free phase closes before the tier changes; neither its zero rates nor its
// runtime caps are copied into PAYG. Explicit subscription overrides survive.
// Paid plans, grants, closures and unresolved purchases retain their arrangement.
func (s *Service) ActivateFundedFree(ctx context.Context, tenantID string) (bool, error) {
	q := purserdb.New(s.db)
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := q.GetFundedFreeCandidate(ctx, tenantID); errors.Is(err, sql.ErrNoRows) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		phase, err := PreparePostpaidPhaseClose(ctx, s.db, s.logger, s, tenantID)
		if err != nil {
			if _, currentErr := q.GetFundedFreeCandidate(ctx, tenantID); errors.Is(currentErr, sql.ErrNoRows) {
				return false, nil
			}
			return false, RecordBillingPhaseTaxReview(ctx, err)
		}
		changed := false
		err = withTx(ctx, s.db, func(tx *sql.Tx) error {
			changed = false
			locked := purserdb.New(tx)
			if _, txErr := locked.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID); txErr != nil {
				if errors.Is(txErr, sql.ErrNoRows) {
					return ErrPostpaidPhaseChanged
				}
				return txErr
			}
			candidate, txErr := locked.GetFundedFreeCandidate(ctx, tenantID)
			if errors.Is(txErr, sql.ErrNoRows) {
				return nil
			}
			if txErr != nil {
				return txErr
			}
			if _, txErr = subscriptions.LockChange(ctx, tx, tenantID, subscriptions.ChangePricing); txErr != nil {
				if subscriptions.IsPricingRefusal(txErr) {
					return nil
				}
				return txErr
			}
			if txErr = subscriptions.CheckGrantCheckout(ctx, tx, tenantID); txErr != nil {
				if errors.Is(txErr, subscriptions.ErrPendingCheckout) || subscriptions.IsPricingRefusal(txErr) {
					return nil
				}
				return txErr
			}
			target, txErr := locked.GetDefaultBillingTier(ctx, true)
			if txErr != nil {
				return fmt.Errorf("load canonical prepaid tier: %w", txErr)
			}
			if target.TierName == "free" {
				return fmt.Errorf("free cannot be the default prepaid tier")
			}
			if phase == nil {
				return ErrPostpaidPhaseChanged
			}
			if _, txErr = phase.CommitTx(ctx, tx); txErr != nil {
				return txErr
			}
			balance, txErr := locked.LockPrepaidBalanceCents(ctx, purserdb.LockPrepaidBalanceCentsParams{TenantID: tenantID, Currency: billing.LedgerCurrency})
			if txErr != nil {
				return txErr
			}
			if balance <= 0 {
				return errFundedFreeBalanceSpent
			}

			if _, txErr = locked.AssignTenantSubscriptionTier(ctx, purserdb.AssignTenantSubscriptionTierParams{TenantID: tenantID, TierID: target.ID.String(), BillingModel: "prepaid"}); txErr != nil {
				return txErr
			}
			if _, txErr = locked.ClearSubscriptionFreeFallback(ctx, tenantID); txErr != nil {
				return txErr
			}
			if _, txErr = ReturnOpenInvoicePrepaidCreditTx(ctx, tx, tenantID); txErr != nil {
				return txErr
			}
			ev, txErr := billingevents.New(tenantID, candidate, &internalv1.SubscriptionUpdated{SubscriptionId: candidate, TierId: target.ID.String(), Status: "active", ChangedFields: []string{"tier_id", "billing_model"}, Reason: "funded_free: settled balance enables prepaid usage"}, events.Actor{})
			if txErr != nil {
				return txErr
			}
			if txErr = emitBillingEventsTx(ctx, tx, "subscription_updated", tenantID, "subscription", candidate, &ipcpb.BillingEvent{SubscriptionId: candidate, Status: "active"}, ev); txErr != nil {
				return txErr
			}
			changed = true
			return nil
		})
		if errors.Is(err, errFundedFreeBalanceSpent) {
			return false, nil
		}
		if errors.Is(err, ErrPostpaidPhaseChanged) {
			continue
		}
		if err != nil {
			return false, RecordBillingPhaseTaxReview(ctx, err)
		}
		if changed {
			phase.AfterCommit(ctx)
		}
		return changed, nil
	}
	return false, ErrPostpaidPhaseChanged
}

var errFundedFreeBalanceSpent = errors.New("funded balance was spent before activation")

// ConvergeTenantEntitlements is safe to repeat after a committed balance credit.
func (s *Service) ConvergeTenantEntitlements(ctx context.Context, tenantID string) error {
	if s.convergeTenantEntitlements == nil {
		return nil
	}
	return s.convergeTenantEntitlements(ctx, tenantID)
}

func (jm *JobManager) runFundedFreeSweep(ctx context.Context) {
	if jm.billing == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		jm.fundedFreeSweepTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (jm *JobManager) fundedFreeSweepTick(ctx context.Context) {
	after := uuid.Nil.String()
	for ctx.Err() == nil {
		candidates, err := purserdb.New(jm.db).ListFundedFreeCandidates(ctx, after)
		if err != nil {
			jm.logger.WithError(err).Warn("Failed to discover funded Free accounts")
			return
		}
		for _, tenantID := range candidates {
			tenantCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			err = jm.billing.ConvergeTenantEntitlements(tenantCtx, tenantID)
			cancel()
			if err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Funded account activation awaits reconciliation")
			}
			after = tenantID
		}
		if len(candidates) < 100 {
			return
		}
	}
}

// Exhaustion has a separate bounded scan so unfunded wallet identities cannot
// delay settled funding. The cursor advances across pages even when ineligible.
func (jm *JobManager) runExhaustedPrepaidSweep(ctx context.Context) {
	if jm.billing == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	after := uuid.Nil.String()
	for {
		after = jm.exhaustedPrepaidSweepPage(ctx, after)
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
		}
	}
}

func (jm *JobManager) exhaustedPrepaidSweepPage(ctx context.Context, after string) string {
	candidates, err := purserdb.New(jm.db).ListExhaustedPrepaidCandidates(ctx, after)
	if err != nil {
		jm.logger.WithError(err).Warn("Failed to discover exhausted prepaid accounts")
		return after
	}
	for _, tenantID := range candidates {
		if ctx.Err() != nil {
			return after
		}
		tenantCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err = jm.billing.ConvergeTenantEntitlements(tenantCtx, tenantID)
		cancel()
		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Exhausted prepaid account awaits reconciliation")
		}
		after = tenantID
	}
	if len(candidates) < 100 {
		return uuid.Nil.String()
	}
	return after
}
