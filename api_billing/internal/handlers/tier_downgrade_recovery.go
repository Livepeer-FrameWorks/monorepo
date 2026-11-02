package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// What holds a downgrade past its effective time, and the outcomes of an
// operator request to release it. DowngradeRecoveryDue is preview only: the
// closing invoice is finalized and nothing refuses the tier change.
const (
	DowngradeRecoveryApplied          = "applied"
	DowngradeRecoveryInvoiceHeld      = "invoice_held"
	DowngradeRecoveryAwaitingProvider = "awaiting_provider"
	DowngradeRecoveryRefused          = "refused"
	DowngradeRecoveryDue              = "due"
)

// ErrNoTierDowngradeHold means the tenant has no downgrade past its effective time.
var ErrNoTierDowngradeHold = errors.New("tenant has no downgrade past its effective time")

// TierDowngradeRecovery is a held downgrade and what holds it.
type TierDowngradeRecovery struct {
	Hold   purserdb.GetTierDowngradeHoldRow
	Status string
	Detail string
}

// TierDowngradeHold reads a downgrade past its effective time and explains what holds it.
func (s *Service) TierDowngradeHold(ctx context.Context, tenantID string) (TierDowngradeRecovery, error) {
	hold, err := purserdb.New(s.db).GetTierDowngradeHold(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return TierDowngradeRecovery{}, ErrNoTierDowngradeHold
	}
	if err != nil {
		return TierDowngradeRecovery{}, fmt.Errorf("read downgrade hold: %w", err)
	}
	return s.describeTierDowngradeHold(ctx, hold)
}

// RecoverHeldTierDowngrade closes the held period again and runs the downgrade
// applier for one tenant, then reports the downgrade as it stands. The applier
// keeps every hold it enforces on its schedule; this only runs it now.
func (s *Service) RecoverHeldTierDowngrade(ctx context.Context, tenantID string) (TierDowngradeRecovery, error) {
	if s.recoverHeldDowngrade == nil {
		return TierDowngradeRecovery{}, errors.New("downgrade recovery needs the billing job manager")
	}
	before, err := s.TierDowngradeHold(ctx, tenantID)
	if err != nil {
		return TierDowngradeRecovery{}, err
	}
	if err = s.recoverHeldDowngrade(ctx, tenantID); err != nil {
		return TierDowngradeRecovery{}, err
	}
	after, err := s.TierDowngradeHold(ctx, tenantID)
	if err == nil && after.Status == DowngradeRecoveryDue {
		// The applier ran and kept the downgrade without a pricing refusal.
		after.Status = DowngradeRecoveryRefused
		after.Detail = fmt.Sprintf("Invoice %s is %s, but the applier did not flip the tier; Purser logs why as \"Pending downgrade held\".", after.Hold.InvoiceID, after.Hold.InvoiceStatus)
	}
	if !errors.Is(err, ErrNoTierDowngradeHold) {
		return after, err
	}
	current, err := purserdb.New(s.db).GetPendingDowngrade(ctx, tenantID)
	if err != nil {
		return TierDowngradeRecovery{}, fmt.Errorf("read applied tier: %w", err)
	}
	if current.TierID != before.Hold.TargetTierID {
		return TierDowngradeRecovery{Hold: before.Hold, Status: DowngradeRecoveryRefused,
			Detail: "The downgrade is no longer pending and the tenant is not on its target tier; the plan changed while the request ran."}, nil
	}
	return TierDowngradeRecovery{Hold: before.Hold, Status: DowngradeRecoveryApplied,
		Detail: fmt.Sprintf("The tenant is on %s and its cluster access follows that tier.", before.Hold.TargetTierName)}, nil
}

// describeTierDowngradeHold names the first condition the applier waits on,
// in the order it checks them.
func (s *Service) describeTierDowngradeHold(ctx context.Context, hold purserdb.GetTierDowngradeHoldRow) (TierDowngradeRecovery, error) {
	out := TierDowngradeRecovery{Hold: hold}
	switch {
	case !hold.ProviderConfirmed:
		out.Status = DowngradeRecoveryAwaitingProvider
		out.Detail = "The payment provider has not confirmed the target price. The tier_downgrade step in admin billing recovery list shows its retries and last error."
	case hold.InvoiceStatus == "manual_review":
		out.Status = DowngradeRecoveryInvoiceHeld
		out.Detail = fmt.Sprintf("Invoice %s is in manual review because a cluster's pricing for the period is unresolved; Purser logs each cluster with \"Invoice routed to manual_review\". Configure that pricing with admin billing set-cluster-pricing, then run this command again.", hold.InvoiceID)
	case hold.InvoiceStatus == "draft":
		out.Status = DowngradeRecoveryInvoiceHeld
		out.Detail = fmt.Sprintf("Invoice %s is still a draft: the period's metering is incomplete or its rating failed, and Purser logs the cause. Run this command again once metering is complete.", hold.InvoiceID)
	case hold.InvoiceID == "":
		out.Status = DowngradeRecoveryInvoiceHeld
		out.Detail = fmt.Sprintf("No invoice closes the period ending %s yet. Confirming closes that period now.", hold.EffectiveAt.UTC().Format(time.RFC3339))
	default:
		refusal, err := s.tierChangeRefusal(ctx, hold.TenantID)
		if err != nil {
			return TierDowngradeRecovery{}, err
		}
		if refusal != "" {
			out.Status = DowngradeRecoveryRefused
			out.Detail = fmt.Sprintf("Invoice %s is %s, but the subscription refuses a tier change: %s.", hold.InvoiceID, hold.InvoiceStatus, refusal)
			break
		}
		out.Status = DowngradeRecoveryDue
		out.Detail = fmt.Sprintf("Invoice %s is %s and nothing refuses the tier change; the daily applier applies the downgrade on its next run, and confirming applies it now.", hold.InvoiceID, hold.InvoiceStatus)
	}
	return out, nil
}

// tierChangeRefusal reports the pricing state that refuses the applier's tier
// flip, reading it under the same lock without changing anything.
func (s *Service) tierChangeRefusal(ctx context.Context, tenantID string) (string, error) {
	var refusal string
	err := database.WithRetryablePostgresRollbackTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		refusal = ""
		_, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.ChangePricing)
		if subscriptions.IsPricingRefusal(lockErr) {
			refusal = lockErr.Error()
			return nil
		}
		return lockErr
	})
	if err != nil {
		return "", fmt.Errorf("read subscription pricing state: %w", err)
	}
	return refusal, nil
}

// recoverHeldDowngrade runs one tenant's period close and the downgrade applier
// with the same gates as the daily invoice job.
func (jm *JobManager) recoverHeldDowngrade(ctx context.Context, tenantID string) error {
	now := time.Now()
	queries := purserdb.New(jm.db)
	due, err := queries.GetSubscriptionDueForInvoice(ctx, purserdb.GetSubscriptionDueForInvoiceParams{TenantID: tenantID, Now: now})
	switch {
	case err == nil:
		jm.finalizeSubscriptionPeriods(ctx, []purserdb.ListSubscriptionsDueForInvoiceRow{purserdb.ListSubscriptionsDueForInvoiceRow(due)}, now, time.Time{})
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read subscription due for invoice: %w", err)
	}
	// The applier itself does not check the closing invoice; this gate does.
	downgradeDue, err := queries.IsPendingDowngradeDueForTenant(ctx, purserdb.IsPendingDowngradeDueForTenantParams{
		TenantID: tenantID, Now: sql.NullTime{Time: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("read due pending downgrade: %w", err)
	}
	if downgradeDue {
		jm.applyPendingDowngrade(ctx, tenantID)
	}
	return nil
}
