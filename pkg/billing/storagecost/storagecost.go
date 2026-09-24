// Package storagecost projects per-asset storage cost from a tenant's tier
// pricing. Callers fetch the tier's marginal price via
// purser.GetTenantBillingStatus (StoragePricing field) and apply Project to
// individual asset byte counts to produce $/day and $/month figures for the
// customer-facing storage browser.
//
// The projection is intentionally simple: marginal $/GiB-month * bytes / 1 GiB,
// with the day figure prorated from the same 730-hour month rating uses. We
// expose the marginal rate (the price the customer would actually save by
// deleting one asset) rather than a blended rate that's harder to reason about.
package storagecost

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// bytesPerGiB matches the canonical storage_gb_seconds ledger unit.
const bytesPerGiB = 1024 * 1024 * 1024

// Projection is the customer-facing cost for a single asset.
type Projection struct {
	PerDay   float64 // currency units per day
	PerMonth float64 // currency units per month
	Currency string  // e.g. "EUR"; empty when pricing is nil
}

// Project returns the marginal cost to keep `bytes` of storage on the tenant's
// tier for one day and one month. Returns the zero Projection when pricing is
// nil or unit price is zero — both render as "$0.00" or "operator-absorbed"
// upstream (self-hosted / marketplace clusters should pass nil pricing).
func Project(pricing *purserpb.StoragePricing, bytes int64) Projection {
	if pricing == nil || pricing.GetUnitPricePerGibMonth() <= 0 || bytes <= 0 {
		if pricing != nil {
			return Projection{Currency: pricing.GetCurrency()}
		}
		return Projection{}
	}
	gb := float64(bytes) / float64(bytesPerGiB)
	perMonth := gb * pricing.GetUnitPricePerGibMonth()
	return Projection{
		PerDay:   perMonth * 24 / billing.HoursPerBillingMonth,
		PerMonth: perMonth,
		Currency: pricing.GetCurrency(),
	}
}
