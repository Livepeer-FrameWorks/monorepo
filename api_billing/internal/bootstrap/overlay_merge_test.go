package bootstrap

import "testing"

// mergeTier overlays a rendered BillingTier onto an embedded CatalogTier:
// any non-zero overlay field replaces the baseline; zero/empty fields fall back.
func TestMergeTier(t *testing.T) {
	base := CatalogTier{
		TierName:    "pro",
		DisplayName: "Pro",
		BasePrice:   10,
		Currency:    "EUR",
		TierLevel:   2,
	}

	t.Run("empty overlay is identity", func(t *testing.T) {
		got, err := mergeTier(base, BillingTier{ID: "pro"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.TierName != base.TierName || got.DisplayName != base.DisplayName ||
			got.BasePrice != base.BasePrice || got.Currency != base.Currency || got.TierLevel != base.TierLevel {
			t.Errorf("empty overlay changed base: got %+v, want %+v", got, base)
		}
	})

	t.Run("non-zero fields override", func(t *testing.T) {
		got, err := mergeTier(base, BillingTier{ID: "pro", DisplayName: "Pro Plus", BasePriceMonthly: "25.00", Currency: "USD"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.DisplayName != "Pro Plus" || got.BasePrice != 25 || got.Currency != "USD" {
			t.Errorf("overlay fields not applied: %+v", got)
		}
		if got.TierName != "pro" || got.TierLevel != 2 {
			t.Errorf("unset overlay fields should fall back to base: %+v", got)
		}
	})

	// TierLevel 0 is the overlay's "unset" value, so the baseline tier level
	// wins rather than writing zero.
	t.Run("zero numeric cannot override (known limitation)", func(t *testing.T) {
		got, err := mergeTier(base, BillingTier{ID: "pro", TierLevel: 0})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.TierLevel != 2 {
			t.Errorf("TierLevel = %d; expected base 2 to win because 0 reads as unset", got.TierLevel)
		}
	})

	t.Run("invalid money fails the merge", func(t *testing.T) {
		if _, err := mergeTier(base, BillingTier{ID: "pro", BasePriceMonthly: "not-money"}); err == nil {
			t.Error("expected error for unparseable base_price_monthly")
		}
	})
}

// TestMergeBillingTierOverlay pins the catalog-merge entry point and the
// addition path (fromOverlay → featuresFromList / pricingRulesFromOverlay) that
// mergeTier's own test does not reach. The documented invariants: empty overlay
// is a no-op; a new ID is appended as a fresh row after the embedded rows
// (ordering preserved); a collision with override=false is a hard error rather
// than silent passthrough; and an empty overlay ID is rejected.
func TestMergeBillingTierOverlay(t *testing.T) {
	embedded := []CatalogTier{
		{TierName: "free", DisplayName: "Free", TierLevel: 1},
		{TierName: "pro", DisplayName: "Pro", BasePrice: 10, Currency: "EUR", TierLevel: 2},
	}

	t.Run("empty overlay returns embedded unchanged", func(t *testing.T) {
		got, err := MergeBillingTierOverlay(embedded, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != len(embedded) {
			t.Fatalf("len = %d, want %d", len(got), len(embedded))
		}
	})

	t.Run("new id is appended via fromOverlay", func(t *testing.T) {
		overlay := []BillingTier{{
			ID:               "enterprise",
			DisplayName:      "Enterprise",
			TierLevel:        3,
			BasePriceMonthly: "199.00",
			Currency:         "EUR",
			Features:         []string{"sso", "priority_support"},
			PricingRules: []OverlayPricingRule{
				{Meter: "egress_gb", Model: "per_unit", UnitPrice: "0.02"},
			},
		}}
		got, err := MergeBillingTierOverlay(embedded, overlay)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3 (embedded preserved + 1 addition)", len(got))
		}
		// Ordering: embedded rows first, addition last.
		if got[0].TierName != "free" || got[1].TierName != "pro" {
			t.Fatalf("embedded ordering not preserved: %s, %s", got[0].TierName, got[1].TierName)
		}
		added := got[2]
		if added.TierName != "enterprise" || added.BasePrice != 199 || added.TierLevel != 3 {
			t.Fatalf("addition mapped wrong: %+v", added)
		}
		// featuresFromList projects []string -> map[string]any{feature: true}.
		if added.Features["sso"] != true || added.Features["priority_support"] != true {
			t.Fatalf("features not projected to map: %+v", added.Features)
		}
		// pricingRulesFromOverlay converts field-for-field.
		if len(added.PricingRules) != 1 || added.PricingRules[0].UnitPrice != "0.02" {
			t.Fatalf("pricing rules not converted: %+v", added.PricingRules)
		}
	})

	t.Run("addition without features/rules yields nil maps", func(t *testing.T) {
		got, err := MergeBillingTierOverlay(embedded, []BillingTier{{ID: "lite", BasePriceMonthly: "5.00"}})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		added := got[len(got)-1]
		if added.Features != nil {
			t.Fatalf("empty feature list must yield nil map, got %+v", added.Features)
		}
		if added.PricingRules != nil {
			t.Fatalf("empty rules must yield nil slice, got %+v", added.PricingRules)
		}
	})

	t.Run("collision with override=false is rejected", func(t *testing.T) {
		if _, err := MergeBillingTierOverlay(embedded, []BillingTier{{ID: "pro", DisplayName: "x"}}); err == nil {
			t.Fatal("expected error colliding with embedded catalog when override=false")
		}
	})

	t.Run("empty overlay id is rejected", func(t *testing.T) {
		if _, err := MergeBillingTierOverlay(embedded, []BillingTier{{ID: ""}}); err == nil {
			t.Fatal("expected error for empty overlay id")
		}
	})
}
