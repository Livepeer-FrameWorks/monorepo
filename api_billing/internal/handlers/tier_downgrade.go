package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// FreeDowngradeStopLead is how long before the effective time a move to Free
// stops the provider subscription. Mollie has no undo, so the subscription stays
// live for a re-point until then, with the lead left for retries before renewal.
const FreeDowngradeStopLead = 72 * time.Hour

// HeldDowngradeGrace outlasts the daily applier cycle, so a confirmed downgrade
// older than this is held rather than waiting for its next run.
const HeldDowngradeGrace = 26 * time.Hour

// ReleaseFreeDowngradeStopTx withdraws a move to Free whose provider stop has not
// started, keeping the subscription live for the tier replacing the downgrade.
func (s *Service) ReleaseFreeDowngradeStopTx(ctx context.Context, tx *sql.Tx, tenantID string) error {
	q := purserdb.New(tx)
	target, err := q.GetProviderTierTarget(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read provider tier target: %w", err)
	}
	if !target.DowngradePending || !target.TargetIsFree || !target.ProviderStopScheduled {
		return nil
	}
	_, err = q.ClearSubscriptionFreeFallback(ctx, tenantID)
	return err
}

// RequestProviderTierSyncTx queues the durable provider step with the change
// that requires it. A staged downgrade the provider cannot charge is refused
// here rather than promised and stalled.
func (s *Service) RequestProviderTierSyncTx(ctx context.Context, tx *sql.Tx, tenantID string) error {
	q := purserdb.New(tx)
	target, err := q.GetProviderTierTarget(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read provider tier target: %w", err)
	}
	if target.StripeSubscriptionID == "" && target.MollieSubscriptionID == "" {
		return nil
	}
	if target.DowngradePending {
		if refusal := s.providerTierRefusal(target); refusal != nil {
			return refusal
		}
	}
	return q.EnqueueProviderTierSync(ctx, tenantID)
}

// providerTierRefusal explains why changing a recurring price cannot reach the tier.
func (s *Service) providerTierRefusal(target purserdb.GetProviderTierTargetRow) error {
	if target.Currency != "EUR" {
		return fmt.Errorf("%w: native subscriptions charge EUR tiers", billing.ErrProviderTierRefused)
	}
	if target.TargetIsFree {
		return nil
	}
	if target.BaseCents <= 0 {
		return fmt.Errorf("%w: the only tier without a monthly fee a provider subscription can move to is Free", billing.ErrProviderTierRefused)
	}
	if target.StripeSubscriptionID != "" {
		if s.stripeClient == nil || target.StripePriceID == "" || target.StripeCustomerID == "" {
			return fmt.Errorf("%w: Stripe monthly collection and a target monthly price are required", billing.ErrProviderTierRefused)
		}
		return nil
	}
	if s.mollieClient == nil || target.MollieCustomerID == "" {
		return fmt.Errorf("%w: Mollie monthly collection is required", billing.ErrProviderTierRefused)
	}
	return nil
}

// ReconcileProviderTier makes the recurring provider subscription match the
// latest desired tier and confirms it. The provider call runs outside the
// subscription lock. A desired tier that moved during the call wakes this step
// again instead of confirming a stale target. A move to Free changes no price:
// inside its lead this schedules the Free fallback, whose sweep stops the provider
// with its retries and alert and retains the paid term through the effective time.
func (s *Service) ReconcileProviderTier(ctx context.Context, tenantID string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var target purserdb.GetProviderTierTargetRow
	act := false
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		var read error
		target, act, read = readProviderTierTarget(ctx, tx, tenantID)
		if read != nil || !act || !target.DowngradePending || !target.TargetIsFree {
			return read
		}
		act = false
		// A payment-failure fallback keeps its date, reason and recovery check.
		if target.FallbackScheduled || !target.PendingEffectiveAt.Valid || target.PendingEffectiveAt.Time.After(time.Now().Add(FreeDowngradeStopLead)) {
			return nil
		}
		// Backdated past clock skew, so the sweep's first read finds it due. The
		// fallback date only starts the provider stop; the paid term sets the move.
		_, read = purserdb.New(tx).StartSubscriptionFreeFallback(ctx, purserdb.StartSubscriptionFreeFallbackParams{
			FallbackAt: time.Now().Add(-time.Minute), Reason: subscriptions.FallbackCancelled, TenantID: tenantID,
		})
		return read
	})
	if err != nil || !act {
		return err
	}
	if target.DowngradePending {
		if refusal := s.providerTierRefusal(target); refusal != nil {
			return refusal
		}
	}
	// With no downgrade pending the provider decides from what it holds: a tier
	// it cannot return to surfaces as a refusal, so the inbox retries and alerts
	// and the unfinished row keeps metadata from moving the local tier.
	request := billing.ProviderTierRequest{TenantID: tenantID, TierID: target.TierID, CurrentTierID: target.CurrentTierID, Downgrade: target.DowngradePending, BaseCents: target.BaseCents}
	var receipt billing.ProviderTierReceipt
	if target.StripeSubscriptionID != "" {
		if s.stripeClient == nil {
			return errors.New("stripe collection is not configured")
		}
		request.SubscriptionID, request.CustomerID, request.PriceID = target.StripeSubscriptionID, target.StripeCustomerID, target.StripePriceID
		receipt, err = s.stripeClient.SyncSubscriptionTier(ctx, request)
	} else {
		if s.mollieClient == nil {
			return errors.New("mollie collection is not configured")
		}
		request.SubscriptionID, request.CustomerID = target.MollieSubscriptionID, target.MollieCustomerID
		receipt, err = s.mollieClient.SyncSubscriptionTier(ctx, request)
	}
	if err != nil {
		return fmt.Errorf("sync provider tier: %w", err)
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		current, stillActing, read := readProviderTierTarget(ctx, tx, tenantID)
		if read != nil || !stillActing {
			return read
		}
		q := purserdb.New(tx)
		if current.TierID != target.TierID || current.StripeSubscriptionID != target.StripeSubscriptionID || current.MollieSubscriptionID != target.MollieSubscriptionID {
			return q.EnqueueProviderTierSync(ctx, tenantID)
		}
		return q.ConfirmProviderTier(ctx, purserdb.ConfirmProviderTierParams{
			TenantID: tenantID, TierID: target.TierID,
			PaidThrough: sql.NullTime{Time: receipt.PaidThrough, Valid: !receipt.PaidThrough.IsZero()},
		})
	})
}

// readProviderTierTarget reports whether a live recurring subscription needs the
// desired tier. Provider cancellation owns a subscription that is ending, and an
// accepted upgrade owns its price until it reconciles. Call it in a transaction.
func readProviderTierTarget(ctx context.Context, tx *sql.Tx, tenantID string) (purserdb.GetProviderTierTargetRow, bool, error) {
	var none purserdb.GetProviderTierTargetRow
	locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
	if errors.Is(err, sql.ErrNoRows) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	if locked.AccountClosed || locked.Current.FreeFallbackProviderStopping || locked.Current.FreeFallbackProviderEndedAt.Valid {
		return none, false, nil
	}
	if upgradeErr := subscriptions.CheckPendingTierUpgrade(ctx, tx, tenantID); upgradeErr != nil {
		if errors.Is(upgradeErr, subscriptions.ErrPendingTierUpgrade) {
			return none, false, fmt.Errorf("%w: %w", ErrAwaitingProviderSettlement, upgradeErr)
		}
		return none, false, upgradeErr
	}
	target, err := purserdb.New(tx).GetProviderTierTarget(ctx, tenantID)
	if err != nil {
		return none, false, err
	}
	return target, target.StripeSubscriptionID != "" || target.MollieSubscriptionID != "", nil
}
