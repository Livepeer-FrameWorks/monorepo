package bootstrap

import (
	"fmt"
	"strings"
)

// Check is the read-only validation pass `purser bootstrap --check` runs after
// parse. It exercises every reference resolvable from the file plus the
// embedded catalog, without touching the database:
//
//   - billing-tier overlay IDs are non-empty;
//   - cluster_pricing entries carry a known pricing_model and a valid
//     base_price (parseMoney rejects partial/garbage numerics);
//   - customer_billing entries reference well-formed tenant refs and a tier
//     slug that exists in the embedded catalog (or, if Override is set, in the
//     overlay).
//
// Cross-service IO (Quartermaster ResolveTenantAliases) only happens in the
// apply path; --check stays offline.
func Check(desired PurserSection, embedded []CatalogTier) error {
	overlaySlugs := make(map[string]struct{})
	for _, t := range desired.BillingTiers {
		if t.ID == "" {
			return fmt.Errorf("billing_tier overlay entry with empty id")
		}
		if t.BasePriceMonthly != "" {
			if _, err := parseMoney(t.BasePriceMonthly); err != nil {
				return fmt.Errorf("billing_tier %q: %w", t.ID, err)
			}
		}
		overlaySlugs[t.ID] = struct{}{}
	}
	knownTiers := make(map[string]struct{}, len(embedded)+len(overlaySlugs))
	for _, t := range embedded {
		knownTiers[t.TierName] = struct{}{}
	}
	for slug := range overlaySlugs {
		knownTiers[slug] = struct{}{}
	}

	for _, cp := range desired.ClusterPricing {
		if cp.ClusterID == "" {
			return fmt.Errorf("cluster_pricing entry with empty cluster_id")
		}
		if !validPricingModel(cp.PricingModel) {
			return fmt.Errorf("cluster_pricing %q: invalid pricing_model %q", cp.ClusterID, cp.PricingModel)
		}
		if cp.BasePrice != "" {
			if _, err := parseMoney(cp.BasePrice); err != nil {
				return fmt.Errorf("cluster_pricing %q: %w", cp.ClusterID, err)
			}
		}
	}

	for _, e := range desired.CustomerBilling {
		if err := validateCustomerBilling(e); err != nil {
			return fmt.Errorf("customer_billing: %w", err)
		}
		if !strings.HasPrefix(e.Tenant.Ref, "quartermaster.") {
			return fmt.Errorf("customer_billing[%s]: tenant ref must point at quartermaster (got %q)", e.Tier, e.Tenant.Ref)
		}
		if _, ok := knownTiers[e.Tier]; !ok {
			return fmt.Errorf("customer_billing[%s]: tier %q not in embedded catalog or overlay", e.Tenant.Ref, e.Tier)
		}
	}
	return nil
}
