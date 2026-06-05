package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
)

// fakeScan returns a scan closure that writes the given string values into the
// destination *string pointers, mimicking sql.Rows.Scan for scanRule.
func fakeScan(vals ...string) func(...any) error {
	return func(dst ...any) error {
		for i := range dst {
			if i < len(vals) {
				*(dst[i].(*string)) = vals[i]
			}
		}
		return nil
	}
}

// scanRule is the gate that turns a stored catalog row into a validated pricing
// rule. The invariant (per its doc) is that a malformed row must FAIL the caller
// rather than be silently repaired — so every parse/validation failure must
// surface as an error, not a zero-valued rule.
func TestScanRule(t *testing.T) {
	t.Run("valid graduated rule", func(t *testing.T) {
		rule, err := scanRule(fakeScan("egress_gb", "tiered_graduated", "EUR", "100", "0.05", "{}"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rule.Meter != rating.MeterEgressGB || rule.Model != rating.ModelTieredGraduated || rule.Currency != "EUR" {
			t.Errorf("rule shape wrong: %+v", rule)
		}
		if !rule.IncludedQuantity.Equal(decimal.NewFromInt(100)) || !rule.UnitPrice.Equal(decimal.RequireFromString("0.05")) {
			t.Errorf("decimals wrong: inc=%s price=%s", rule.IncludedQuantity, rule.UnitPrice)
		}
		if rule.Config != nil {
			t.Errorf(`config "{}" should decode to nil, got %v`, rule.Config)
		}
	})

	t.Run("codec multiplier config decoded", func(t *testing.T) {
		rule, err := scanRule(fakeScan("media_seconds", "codec_multiplier", "EUR", "0", "0.01", `{"codec_multipliers":{"h264":1.5}}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := rule.Config["codec_multipliers"]; !ok {
			t.Errorf("expected codec_multipliers in config, got %v", rule.Config)
		}
	})

	t.Run("malformed inputs must error, not repair", func(t *testing.T) {
		cases := []struct {
			name string
			scan func(...any) error
		}{
			{"bad included_quantity", fakeScan("egress_gb", "tiered_graduated", "EUR", "abc", "0.05", "{}")},
			{"bad unit_price", fakeScan("egress_gb", "tiered_graduated", "EUR", "100", "xyz", "{}")},
			{"bad config json", fakeScan("egress_gb", "tiered_graduated", "EUR", "100", "0.05", "{nope")},
			// Models are a CLOSED enum: an unknown model is rejected.
			{"unknown model fails validation", fakeScan("egress_gb", "bogus_model", "EUR", "100", "0.05", "{}")},
			// Meters are syntactically validated: bad characters are rejected.
			{"syntactically invalid meter", fakeScan("Bad Meter!", "tiered_graduated", "EUR", "100", "0.05", "{}")},
			{"negative unit price fails validation", fakeScan("egress_gb", "tiered_graduated", "EUR", "100", "-1", "{}")},
			{"empty currency fails validation", fakeScan("egress_gb", "tiered_graduated", "", "100", "0.05", "{}")},
		}
		for _, tc := range cases {
			if _, err := scanRule(tc.scan); err == nil {
				t.Errorf("%s: expected error, got nil", tc.name)
			}
		}
	})

	// Meters are an OPEN vocabulary (syntactic validation only), so a novel but
	// well-formed meter name is accepted — this asymmetry with the closed Model
	// enum is intentional.
	t.Run("novel well-formed meter is accepted", func(t *testing.T) {
		if _, err := scanRule(fakeScan("future_meter_v2", "all_usage", "EUR", "0", "0.01", "{}")); err != nil {
			t.Errorf("expected a syntactically valid meter to be accepted, got %v", err)
		}
	})

	t.Run("scan error propagates", func(t *testing.T) {
		boom := errors.New("scan failed")
		if _, err := scanRule(func(...any) error { return boom }); !errors.Is(err, boom) {
			t.Errorf("expected scan error to propagate, got %v", err)
		}
	})
}

func TestDecodeJSONMap(t *testing.T) {
	if m, err := decodeJSONMap(""); err != nil || m != nil {
		t.Errorf("empty string: got (%v, %v), want (nil, nil)", m, err)
	}
	if m, err := decodeJSONMap("   "); err != nil || m != nil {
		t.Errorf("whitespace: got (%v, %v), want (nil, nil)", m, err)
	}
	m, err := decodeJSONMap(`{"a":1,"b":"x"}`)
	if err != nil || m["a"] != float64(1) || m["b"] != "x" {
		t.Errorf("valid json: got (%v, %v)", m, err)
	}
	if _, err := decodeJSONMap("{not json"); err == nil {
		t.Error("invalid json should error")
	}
}

func egressBase() rating.Rule {
	return rating.Rule{
		Meter:            rating.MeterEgressGB,
		Model:            rating.ModelTieredGraduated,
		Currency:         "EUR",
		IncludedQuantity: decimal.NewFromInt(100),
		UnitPrice:        decimal.RequireFromString("0.05"),
	}
}

func overrideRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"})
}

// applyPricingOverrides shadows tier rules per meter: an override replaces the
// rule wholesale, but unset fields fall back to the base rule; an override for a
// meter not on the tier is appended. This is what decides the final charged
// price, so the merge semantics are pinned exactly.
func TestApplyPricingOverrides_PartialFallbackAndAddition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rows := overrideRows().
		// Partial override of an existing meter: only unit_price set.
		AddRow("egress_gb", nil, nil, nil, "0.03", nil).
		// New meter not on the tier: fully specified.
		AddRow("ingress_gb", "all_usage", "EUR", "0", "0.02", nil).
		// Empty meter must be skipped.
		AddRow("", nil, nil, nil, "9.99", nil)
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(rows)

	out, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBase()})
	if err != nil {
		t.Fatalf("applyPricingOverrides: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 rules (overridden egress + added ingress), got %d: %+v", len(out), out)
	}

	byMeter := map[rating.Meter]rating.Rule{}
	for _, r := range out {
		byMeter[r.Meter] = r
	}
	eg, ok := byMeter[rating.MeterEgressGB]
	if !ok {
		t.Fatal("egress_gb rule missing")
	}
	// Overridden field.
	if !eg.UnitPrice.Equal(decimal.RequireFromString("0.03")) {
		t.Errorf("egress unit_price = %s, want 0.03 (overridden)", eg.UnitPrice)
	}
	// Fields that fall back to the base rule.
	if !eg.IncludedQuantity.Equal(decimal.NewFromInt(100)) || eg.Model != rating.ModelTieredGraduated || eg.Currency != "EUR" {
		t.Errorf("egress fallbacks wrong: %+v", eg)
	}
	ing, ok := byMeter[rating.MeterIngressGB]
	if !ok {
		t.Fatal("ingress_gb (added) rule missing")
	}
	if !ing.UnitPrice.Equal(decimal.RequireFromString("0.02")) || ing.Model != rating.ModelAllUsage {
		t.Errorf("added ingress rule wrong: %+v", ing)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestApplyPricingOverrides_NoOverridesReturnsBase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(overrideRows())

	out, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBase()})
	if err != nil {
		t.Fatalf("applyPricingOverrides: %v", err)
	}
	if len(out) != 1 || out[0].Meter != rating.MeterEgressGB || !out[0].UnitPrice.Equal(decimal.RequireFromString("0.05")) {
		t.Errorf("empty overrides should return base unchanged, got %+v", out)
	}
}

// A malformed override (here, a negative unit_price that fails validation) must
// fail the whole resolution rather than silently mis-price a tenant.
func TestApplyPricingOverrides_InvalidOverrideErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	rows := overrideRows().AddRow("egress_gb", nil, nil, nil, "-1", nil)
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(rows)

	if _, err := applyPricingOverrides(context.Background(), db, "sub-1", []rating.Rule{egressBase()}); err == nil {
		t.Error("expected error for an override that fails rule validation")
	}
}

func TestLoadEffectiveTier_GuardsBadInput(t *testing.T) {
	if _, err := LoadEffectiveTier(context.Background(), nil, "tenant-1"); err == nil {
		t.Error("nil db should error")
	}
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	if _, err := LoadEffectiveTier(context.Background(), db, ""); err == nil {
		t.Error("empty tenant_id should error")
	}
}

// applyEntitlementOverrides shadows tier entitlements per key: an override
// replaces that key's value, untouched keys are kept, and new keys are added.
func TestApplyEntitlementOverrides(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery("subscription_entitlement_overrides").WithArgs("sub-1").WillReturnRows(
		sqlmock.NewRows([]string{"key", "value"}).
			AddRow("dvr_max_entries", "10"). // override existing
			AddRow("max_clusters", "3"),     // add new
	)

	base := map[string]string{"dvr_max_entries": "5", "retention_days": "7"}
	out, err := applyEntitlementOverrides(context.Background(), db, "sub-1", base)
	if err != nil {
		t.Fatalf("applyEntitlementOverrides: %v", err)
	}
	if out["dvr_max_entries"] != "10" {
		t.Errorf("dvr_max_entries = %q, want overridden 10", out["dvr_max_entries"])
	}
	if out["retention_days"] != "7" {
		t.Errorf("retention_days = %q, want base 7", out["retention_days"])
	}
	if out["max_clusters"] != "3" {
		t.Errorf("max_clusters = %q, want added 3", out["max_clusters"])
	}
	if base["dvr_max_entries"] != "5" {
		t.Errorf("base map mutated: %v", base) // overrides must not mutate the caller's map
	}
}

// LoadEffectiveTier is the public contract every rated invoice depends on: it
// resolves the active subscription's tier, parses the base price, loads + merges
// pricing rules and entitlements. This pins the full query sequence end to end.
func TestLoadEffectiveTier_ResolvesActiveSubscription(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("tenant_subscriptions").WithArgs("tenant-1").WillReturnRows(
		sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).
			AddRow("tier-pro", "pro", "10.00", "EUR", true, "sub-1"),
	)
	mock.ExpectQuery("tier_pricing_rules").WithArgs("tier-pro").WillReturnRows(
		sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("egress_gb", "tiered_graduated", "EUR", "100", "0.05", "{}"),
	)
	mock.ExpectQuery("subscription_pricing_overrides").WithArgs("sub-1").WillReturnRows(overrideRows())
	mock.ExpectQuery("tier_entitlements").WithArgs("tier-pro").WillReturnRows(
		sqlmock.NewRows([]string{"key", "value"}).AddRow("dvr_max_entries", "5"),
	)
	mock.ExpectQuery("subscription_entitlement_overrides").WithArgs("sub-1").WillReturnRows(
		sqlmock.NewRows([]string{"key", "value"}),
	)

	eff, err := LoadEffectiveTier(context.Background(), db, "tenant-1")
	if err != nil {
		t.Fatalf("LoadEffectiveTier: %v", err)
	}
	if eff.TierID != "tier-pro" || eff.TierName != "pro" || eff.Currency != "EUR" || !eff.MeteringEnabled {
		t.Errorf("tier fields wrong: %+v", eff)
	}
	if !eff.BasePrice.Equal(decimal.RequireFromString("10.00")) {
		t.Errorf("base_price = %s, want 10.00", eff.BasePrice)
	}
	if len(eff.Rules) != 1 || eff.Rules[0].Meter != rating.MeterEgressGB {
		t.Errorf("rules wrong: %+v", eff.Rules)
	}
	if eff.Entitlements["dvr_max_entries"] != "5" {
		t.Errorf("entitlements wrong: %+v", eff.Entitlements)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
