// Package storagecost projects per-asset storage cost from a tenant's tier
// pricing. Callers fetch the tier's marginal price via
// purser.GetTenantBillingStatus (StoragePricing field) and apply Project to
// individual asset byte counts to produce $/day and $/month figures for the
// customer-facing storage browser.
//
// The projection is intentionally simple: marginal $/GB-month * bytes / 1 GB,
// divided by 30 for per-day. We expose the marginal rate (the price the
// customer would actually save by deleting one asset) rather than a blended
// rate that's harder to reason about.
package storagecost

import (
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// bytesPerGB is 1 GB in bytes (decimal). Matches how storage pricing is
// quoted publicly. Distinct from the binary GiB the storage_limit_gb runtime
// cap uses — that one is a hard runtime check, this one is a customer-facing
// cost estimate.
const bytesPerGB = 1_000_000_000

// daysPerMonth normalizes "month" to 30 days for per-day projection. Calendar
// months vary but 30 keeps the UI math stable across the year.
const daysPerMonth = 30

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
func Project(pricing *pb.StoragePricing, bytes int64) Projection {
	if pricing == nil || pricing.GetUnitPricePerGbMonth() <= 0 || bytes <= 0 {
		if pricing != nil {
			return Projection{Currency: pricing.GetCurrency()}
		}
		return Projection{}
	}
	gb := float64(bytes) / float64(bytesPerGB)
	perMonth := gb * pricing.GetUnitPricePerGbMonth()
	return Projection{
		PerDay:   perMonth / daysPerMonth,
		PerMonth: perMonth,
		Currency: pricing.GetCurrency(),
	}
}
