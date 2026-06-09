package billing

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
)

// egressBaseWithConfig is egressBase plus a non-empty Config so override merge
// behavior against empty/"{}" config strings is observable.
func egressBaseWithConfig() rating.Rule {
	r := egressBase()
	r.Config = map[string]any{"description": "base desc"}
	return r
}

// TestApplyPricingOverrides_EmptyIncludedFallsBackNoError pins the
// included_quantity guard: a present-but-empty override string must fall back
// to the base included quantity, not attempt to parse "" (which would error).
func TestApplyPricingOverrides_EmptyIncludedFallsBackNoError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := overrideRows().AddRow("egress_gb", nil, nil, "", "0.03", nil)
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(rows)

	out, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBase()})
	if err != nil {
		t.Fatalf("applyPricingOverrides: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(out))
	}
	if !out[0].UnitPrice.Equal(decimal.RequireFromString("0.03")) {
		t.Errorf("unit_price = %s, want 0.03 (overridden)", out[0].UnitPrice)
	}
	if !out[0].IncludedQuantity.Equal(decimal.NewFromInt(100)) {
		t.Errorf("included = %s, want 100 (empty override falls back to base)", out[0].IncludedQuantity)
	}
}

// TestApplyPricingOverrides_SetIncludedApplies pins that a real included
// quantity in the override row replaces the base value.
func TestApplyPricingOverrides_SetIncludedApplies(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := overrideRows().AddRow("egress_gb", nil, nil, "250", nil, nil)
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(rows)

	out, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBase()})
	if err != nil {
		t.Fatalf("applyPricingOverrides: %v", err)
	}
	if !out[0].IncludedQuantity.Equal(decimal.NewFromInt(250)) {
		t.Errorf("included = %s, want 250 (overridden)", out[0].IncludedQuantity)
	}
}

// TestApplyPricingOverrides_ConfigEmptyAndBracesPreserveBase pins the config
// merge guard: neither an empty config string nor "{}" replaces the base rule's
// config — both are treated as "no config override" and the base config is kept.
func TestApplyPricingOverrides_ConfigEmptyAndBracesPreserveBase(t *testing.T) {
	cases := []struct {
		name      string
		configVal any
	}{
		{"empty string", ""},
		{"empty object", "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			rows := overrideRows().AddRow("egress_gb", nil, nil, nil, "0.03", tc.configVal)
			mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(rows)

			out, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBaseWithConfig()})
			if err != nil {
				t.Fatalf("applyPricingOverrides: %v", err)
			}
			if out[0].Config == nil {
				t.Fatalf("config was wiped; want base config preserved")
			}
			if out[0].Config["description"] != "base desc" {
				t.Errorf("config = %v, want base config {description:base desc} preserved", out[0].Config)
			}
		})
	}
}

// TestApplyPricingOverrides_RealConfigReplacesBase pins that a real config JSON
// override does replace the base rule's config.
func TestApplyPricingOverrides_RealConfigReplacesBase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := overrideRows().AddRow("egress_gb", nil, nil, nil, "0.03", `{"description":"override desc"}`)
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(rows)

	out, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBaseWithConfig()})
	if err != nil {
		t.Fatalf("applyPricingOverrides: %v", err)
	}
	if out[0].Config["description"] != "override desc" {
		t.Errorf("config = %v, want override applied", out[0].Config)
	}
}
