package billing

import (
	"errors"
	"time"
)

// TierUpgradeRequest fixes the identity and pricing of one monthly upgrade.
// Providers retain this identity so a lost response can be located before retrying.
type TierUpgradeRequest struct {
	ID, TenantID, OldTierID, TierID, CustomerID, SubscriptionID, PriceID string
	RequestedAt, OldPeriodStart, OldPeriodEnd                            time.Time
	// LocateOnly reconciles an existing provider update without creating one.
	LocateOnly bool
	BaseCents  int64
	// OldBaseCents snapshots the catalog fee for the local billing boundary.
	// Stripe validates its native paid invoice basis independently.
	OldBaseCents int64
}

// TierUpgradeReceipt records the provider's authoritative replacement period.
// UnusedPaidCents is used when Purser invoices the replacement fee; Stripe
// records its own credit on the provider's replacement invoice instead.
type TierUpgradeReceipt struct {
	PeriodStart, PeriodEnd         time.Time
	PaidPeriodStart, PaidPeriodEnd time.Time
	UnusedPaidCents                int64
	PaidReceiptID                  string
	Applied                        bool
}

// ErrTierUpgradeUnsettled waits for the old paid fee's authoritative outcome.
var ErrTierUpgradeUnsettled = errors.New("the previous subscription payment has not settled")

// ErrTierUpgradeRejected means the provider definitively refused the mutation.
// Transport failures do not imply rejection and must retain the pricing fence.
var ErrTierUpgradeRejected = errors.New("payment provider refused the tier upgrade")

// NextMonthlyAnniversary preserves the time of day and caps a missing calendar
// day at the target month's last day, so January 31 renews on February 28/29.
func NextMonthlyAnniversary(at time.Time) time.Time {
	return AddCalendarMonths(at, 1)
}

// PreviousMonthlyAnniversary finds the prior calendar month's capped day while
// preserving the time of day, so March 31 looks back to February 28/29.
func PreviousMonthlyAnniversary(at time.Time) time.Time {
	return AddCalendarMonths(at, -1)
}

// AddCalendarMonths preserves the time of day and caps a missing day in the
// destination month. Negative offsets move to an earlier calendar month.
func AddCalendarMonths(at time.Time, offset int) time.Time {
	first := time.Date(at.Year(), at.Month()+time.Month(offset), 1, at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), at.Location())
	lastDay := time.Date(first.Year(), first.Month()+1, 0, 0, 0, 0, 0, at.Location()).Day()
	return time.Date(first.Year(), first.Month(), min(at.Day(), lastDay), at.Hour(), at.Minute(), at.Second(), at.Nanosecond(), at.Location())
}

// NextMonthlyDateBoundary rounds a full calendar month up to a UTC date-only
// renewal boundary, so a provider's startDate never shortens the paid month.
func NextMonthlyDateBoundary(at time.Time) time.Time {
	next := NextMonthlyAnniversary(at.UTC())
	boundary := time.Date(next.Year(), next.Month(), next.Day(), 0, 0, 0, 0, time.UTC)
	if boundary.Before(next) {
		boundary = boundary.AddDate(0, 0, 1)
	}
	return boundary
}

// ProviderTierRequest names the recurring price a provider subscription must
// charge from its next renewal. The current paid term is not repriced.
type ProviderTierRequest struct {
	TenantID string
	TierID   string
	// CurrentTierID is the tenant's local tier, which a recorded restore belongs to.
	CurrentTierID string
	// Downgrade is set while a downgrade to TierID is pending. Otherwise the
	// request returns the subscription to its local tier.
	Downgrade      bool
	CustomerID     string
	SubscriptionID string
	PriceID        string
	BaseCents      int64
}

// ProviderTierReceipt reports the provider's state after a tier change.
type ProviderTierReceipt struct {
	// PaidThrough is the end of the term already collected at the provider.
	PaidThrough time.Time
}

// ErrProviderTierRefused means a subscription cannot be moved to the requested
// tier by changing its recurring price.
var ErrProviderTierRefused = errors.New("the payment provider subscription cannot move to this tier")
