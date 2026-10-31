package subscriptions

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_billing/internal/database/purserdb"
)

// ErrSelfServiceTier refuses a tier that cannot be purchased through self-service.
var ErrSelfServiceTier = errors.New("self-service requires an active, non-Enterprise postpaid tier with level at least one")

// ErrSelfServiceTierChange preserves the paid period until its upgrade is staged.
var ErrSelfServiceTierChange = errors.New("use ChangeBillingTier for a paid advance subscription change during its current period")

// ErrSelfServiceSubscription preserves the established provider subscription.
var ErrSelfServiceSubscription = errors.New("a payment-provider subscription is still live; use ChangeBillingTier instead of another checkout")

// ValidateSelfServiceTier applies the catalog rule shared by purchase and activation.
func ValidateSelfServiceTier(tier purserdb.GetTierForBillingChangeRow) error {
	if !tier.IsActive.Bool || tier.IsEnterprise || tier.IsDefaultPrepaid.Bool || tier.TierLevel.Int32 < 1 {
		return ErrSelfServiceTier
	}
	return nil
}

// CheckSelfServiceCheckout refuses a second purchase while a paid subscription is live.
func CheckSelfServiceCheckout(ctx context.Context, exec purserdb.DBTX, tenantID, tierID, provider string) error {
	return CheckSelfServiceActivation(ctx, exec, tenantID, tierID, provider, "")
}

// CheckSelfServiceActivation rechecks the catalog and paid period under the
// subscription lock. Only a replay of the established provider identity may
// reuse a live native subscription; a different purchase must be compensated.
func CheckSelfServiceActivation(ctx context.Context, exec purserdb.DBTX, tenantID, tierID, provider, incomingID string) error {
	q := purserdb.New(exec)
	tier, err := q.GetTierForBillingChange(ctx, tierID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSelfServiceTier
	}
	if err != nil {
		return err
	}
	if err = ValidateSelfServiceTier(tier); err != nil {
		return err
	}
	changing, err := q.SelfServiceAdvanceTierChange(ctx, purserdb.SelfServiceAdvanceTierChangeParams{TenantID: tenantID, TierID: tierID})
	if err != nil {
		return err
	}
	if changing {
		return ErrSelfServiceTierChange
	}
	native, err := q.GetSelfServiceNativeSubscriptions(ctx, tenantID)
	if err != nil {
		return err
	}
	if native.PaidCancellationTerm {
		return ErrPaidCancellationTerm
	}
	if native.StripeID != "" && (provider != "stripe" || native.StripeID != incomingID) || native.MollieID != "" && (provider != "mollie" || native.MollieID != incomingID) {
		return ErrSelfServiceSubscription
	}
	return nil
}
