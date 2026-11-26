package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ReturnExhaustedPrepaidToFree closes received PAYG usage before restoring Free.
// It preserves the wallet, funding preference and explicit entitlement overrides.
// Debt survives fallback; pending purchases and custom arrangements retain prepaid.
func (s *Service) ReturnExhaustedPrepaidToFree(ctx context.Context, tenantID string) (bool, error) {
	q := purserdb.New(s.db)
	if err := q.InitializeExhaustedPaymentGrace(ctx, tenantID); err != nil {
		return false, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		candidateID, err := q.GetExhaustedPrepaidCandidate(ctx, tenantID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		freeTierID, err := q.GetAutomaticFreeTier(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		limits, err := loadFreeTierLimits(ctx, s.db, freeTierID, candidateID)
		if err != nil {
			return false, err
		}
		if s.freeTierEligibility == nil {
			return false, nil
		}
		eligible, err := s.freeTierEligibility(ctx, tenantID, limits)
		if err != nil {
			return false, fmt.Errorf("check verified identity before Free fallback: %w", err)
		}
		if !eligible {
			return false, nil
		}
		phase, err := PreparePrepaidPhaseClose(ctx, s.db, s.logger, s, tenantID)
		if err != nil {
			if _, currentErr := q.GetExhaustedPrepaidCandidate(ctx, tenantID); errors.Is(currentErr, sql.ErrNoRows) {
				return false, nil
			}
			return false, fmt.Errorf("prepare exhausted prepaid phase: %w", err)
		}
		changed := false
		err = withTx(ctx, s.db, func(tx *sql.Tx) error {
			changed = false
			locked := purserdb.New(tx)
			if _, txErr := locked.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID); txErr != nil {
				return txErr
			}
			candidate, txErr := locked.GetExhaustedPrepaidCandidate(ctx, tenantID)
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
			target, txErr := locked.GetAutomaticFreeTier(ctx)
			if errors.Is(txErr, sql.ErrNoRows) {
				return nil
			}
			if txErr != nil {
				return txErr
			}
			if _, txErr = locked.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{TenantID: tenantID, Currency: billing.LedgerCurrency}); txErr != nil {
				return txErr
			}
			if target != freeTierID {
				return ErrPrepaidPhaseChanged
			}
			currentLimits, limitErr := loadFreeTierLimits(ctx, tx, target, candidate)
			if limitErr != nil {
				return limitErr
			}
			if currentLimits != limits {
				return ErrPrepaidPhaseChanged
			}
			if phase == nil {
				return ErrPrepaidPhaseChanged
			}
			if _, txErr = phase.CommitTx(ctx, tx); txErr != nil {
				return fmt.Errorf("close exhausted prepaid phase: %w", txErr)
			}
			// The statement may settle received usage that has not yet debited the
			// wallet. Recheck after it commits inside this transaction; roll back
			// the whole boundary when that creates debt or returns usable credit.
			if _, txErr = locked.GetExhaustedPrepaidCandidate(ctx, tenantID); errors.Is(txErr, sql.ErrNoRows) {
				return errExhaustedBalanceChanged
			} else if txErr != nil {
				return txErr
			}
			if txErr = locked.BeginFreeMediaRecovery(ctx, tenantID); txErr != nil {
				return txErr
			}
			if _, txErr = locked.MoveExhaustedPrepaidToFree(ctx, purserdb.MoveExhaustedPrepaidToFreeParams{TenantID: tenantID, TierID: target}); txErr != nil {
				return txErr
			}
			ev, txErr := billingevents.New(tenantID, candidate, &internalv1.SubscriptionUpdated{SubscriptionId: candidate, TierId: target, Status: "active", ChangedFields: []string{"tier_id", "billing_model"}, Reason: "payment grace ended: verified account returned to Free limits; unpaid usage retained"}, events.Actor{})
			if txErr != nil {
				return txErr
			}
			if txErr = emitBillingEventsTx(ctx, tx, "subscription_updated", tenantID, "subscription", candidate, &ipcpb.BillingEvent{SubscriptionId: candidate, Status: "active"}, ev); txErr != nil {
				return txErr
			}
			changed = true
			return nil
		})
		if errors.Is(err, errExhaustedBalanceChanged) {
			return false, nil
		}
		if errors.Is(err, ErrPrepaidPhaseChanged) {
			continue
		}
		if err != nil {
			if _, currentErr := q.GetExhaustedPrepaidCandidate(ctx, tenantID); errors.Is(currentErr, sql.ErrNoRows) {
				return false, nil
			}
			return false, fmt.Errorf("commit exhausted prepaid fallback: %w", err)
		}
		if changed {
			phase.Log()
		}
		return changed, nil
	}
	return false, ErrPrepaidPhaseChanged
}

var errExhaustedBalanceChanged = errors.New("prepaid closing statement changed the exhausted balance")
