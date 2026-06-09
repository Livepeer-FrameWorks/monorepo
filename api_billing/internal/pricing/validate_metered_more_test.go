package pricing

import (
	"errors"
	"testing"
)

func TestValidateMeteredRates(t *testing.T) {
	validRate := map[string]any{
		"egress_gb": map[string]any{
			"model":             "all_usage",
			"unit_price":        "0.01",
			"included_quantity": "0",
		},
	}

	t.Run("valid", func(t *testing.T) {
		if err := ValidateMeteredRates(validRate, ModelMetered); err != nil {
			t.Fatalf("valid rates rejected: %v", err)
		}
	})

	t.Run("metered requires at least one rate", func(t *testing.T) {
		if err := ValidateMeteredRates(map[string]any{}, ModelMetered); err == nil {
			t.Fatal("empty metered rates should error")
		}
	})

	t.Run("custom missing rates returns sentinel", func(t *testing.T) {
		err := ValidateMeteredRates(map[string]any{}, ModelCustom)
		if !errors.Is(err, ErrCustomPricingMissingForCluster) {
			t.Fatalf("want ErrCustomPricingMissingForCluster, got %v", err)
		}
	})

	t.Run("invalid meter name", func(t *testing.T) {
		bad := map[string]any{"Bad-Meter": map[string]any{"model": "all_usage", "unit_price": "0.01"}}
		if err := ValidateMeteredRates(bad, ModelMetered); err == nil {
			t.Fatal("malformed meter should error")
		}
	})

	t.Run("rate must be object", func(t *testing.T) {
		bad := map[string]any{"egress_gb": "0.01"}
		if err := ValidateMeteredRates(bad, ModelMetered); err == nil {
			t.Fatal("non-object rate should error")
		}
	})

	t.Run("model required", func(t *testing.T) {
		bad := map[string]any{"egress_gb": map[string]any{"unit_price": "0.01"}}
		if err := ValidateMeteredRates(bad, ModelMetered); err == nil {
			t.Fatal("missing model should error")
		}
	})

	t.Run("unit_price required", func(t *testing.T) {
		bad := map[string]any{"egress_gb": map[string]any{"model": "all_usage"}}
		if err := ValidateMeteredRates(bad, ModelMetered); err == nil {
			t.Fatal("missing unit_price should error")
		}
	})

	// Non-metered models don't require rates and skip the count guard entirely.
	t.Run("tier_inherit allows empty", func(t *testing.T) {
		if err := ValidateMeteredRates(map[string]any{}, ModelTierInherit); err != nil {
			t.Fatalf("tier_inherit with no rates should pass: %v", err)
		}
	})
}
