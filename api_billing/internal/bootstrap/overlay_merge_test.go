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
