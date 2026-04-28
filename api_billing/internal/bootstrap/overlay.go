package bootstrap

import "fmt"

// MergeBillingTierOverlay returns the embedded catalog merged with overlay
// entries from the rendered bootstrap file. Stable key: tier_name == ID.
// Semantics match cli/pkg/bootstrap.BillingTier:
//
//   - Overlay tier whose ID matches an embedded tier and Override=true ⇒
//     field-by-field merge: any non-zero overlay field replaces the embedded
//     value; zero values fall back to the embedded baseline.
//   - Overlay tier whose ID matches an embedded tier and Override=false ⇒
//     hard error (the renderer's validate pass is the first line of defense;
//     this function is the last-resort guard, not silent passthrough).
//   - Overlay tier whose ID does not match any embedded tier ⇒ appended as
//     a fresh catalog row. Override is irrelevant for additions.
//
// The catalog ordering is preserved (embedded first, in YAML order; then
// overlay-only additions in the order they appear in the rendered file).
//
// Money parsing errors propagate; an invalid base_price_monthly fails the
// whole merge rather than silently coercing to 0.
func MergeBillingTierOverlay(embedded []CatalogTier, overlay []BillingTier) ([]CatalogTier, error) {
	if len(overlay) == 0 {
		return embedded, nil
	}
	byName := make(map[string]int, len(embedded))
	for i, t := range embedded {
		byName[t.TierName] = i
	}
	out := make([]CatalogTier, len(embedded))
	copy(out, embedded)

	for _, o := range overlay {
		if o.ID == "" {
			return nil, fmt.Errorf("overlay billing_tier with empty id")
		}
		idx, exists := byName[o.ID]
		if exists && !o.Override {
			return nil, fmt.Errorf("overlay billing_tier %q collides with the embedded catalog and override=false", o.ID)
		}
		if exists {
			merged, err := mergeTier(out[idx], o)
			if err != nil {
				return nil, fmt.Errorf("overlay billing_tier %q: %w", o.ID, err)
			}
			out[idx] = merged
			continue
		}
		fresh, err := fromOverlay(o)
		if err != nil {
			return nil, fmt.Errorf("overlay billing_tier %q: %w", o.ID, err)
		}
		out = append(out, fresh)
		byName[o.ID] = len(out) - 1
	}
	return out, nil
}

func mergeTier(base CatalogTier, o BillingTier) (CatalogTier, error) {
	if o.DisplayName != "" {
		base.DisplayName = o.DisplayName
	}
	if o.TierLevel != 0 {
		base.TierLevel = int(o.TierLevel)
	}
	if o.BasePriceMonthly != "" {
		v, err := parseMoney(o.BasePriceMonthly)
		if err != nil {
			return CatalogTier{}, err
		}
		base.BasePrice = v
	}
	if o.Currency != "" {
		base.Currency = o.Currency
	}
	if len(o.BandwidthAllocation) > 0 {
		base.BandwidthAllocation = o.BandwidthAllocation
	}
	if len(o.StorageAllocation) > 0 {
		base.StorageAllocation = o.StorageAllocation
	}
	if len(o.ComputeAllocation) > 0 {
		base.ComputeAllocation = o.ComputeAllocation
	}
	if len(o.Features) > 0 {
		base.Features = featuresFromList(o.Features)
	}
	if len(o.OverageRates) > 0 {
		base.OverageRates = o.OverageRates
	}
	return base, nil
}

func fromOverlay(o BillingTier) (CatalogTier, error) {
	price, err := parseMoney(o.BasePriceMonthly)
	if err != nil {
		return CatalogTier{}, err
	}
	return CatalogTier{
		TierName:            o.ID,
		DisplayName:         o.DisplayName,
		BasePrice:           price,
		Currency:            o.Currency,
		BandwidthAllocation: o.BandwidthAllocation,
		StorageAllocation:   o.StorageAllocation,
		ComputeAllocation:   o.ComputeAllocation,
		Features:            featuresFromList(o.Features),
		OverageRates:        o.OverageRates,
		TierLevel:           int(o.TierLevel),
	}, nil
}

// featuresFromList projects the overlay's []string feature list into the
// embedded catalog's map[string]any feature shape.
func featuresFromList(list []string) map[string]any {
	if len(list) == 0 {
		return nil
	}
	out := make(map[string]any, len(list))
	for _, f := range list {
		out[f] = true
	}
	return out
}
