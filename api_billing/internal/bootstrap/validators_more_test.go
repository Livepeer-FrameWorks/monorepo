package bootstrap

import "testing"

func TestValidPricingModel(t *testing.T) {
	for _, m := range []string{"free_unmetered", "metered", "monthly", "tier_inherit", "custom"} {
		if !validPricingModel(m) {
			t.Fatalf("%q should be a valid pricing model", m)
		}
	}
	for _, m := range []string{"", "tiered", "postpaid", "Metered"} {
		if validPricingModel(m) {
			t.Fatalf("%q should not be a valid pricing model", m)
		}
	}
}

func TestValidateCatalogPricingRule(t *testing.T) {
	valid := CatalogPricingRule{
		Meter:     "egress_gb",
		Model:     "all_usage",
		Currency:  "USD",
		UnitPrice: "0.01",
	}
	if err := validateCatalogPricingRule(valid, "USD"); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}

	// Currency falls back to the tier currency when the rule omits it.
	noCurrency := valid
	noCurrency.Currency = ""
	if err := validateCatalogPricingRule(noCurrency, "EUR"); err != nil {
		t.Fatalf("rule should inherit tier currency: %v", err)
	}
	if err := validateCatalogPricingRule(noCurrency, ""); err == nil {
		t.Fatal("no currency anywhere should error")
	}

	badMeter := valid
	badMeter.Meter = "Bad-Meter"
	if err := validateCatalogPricingRule(badMeter, "USD"); err == nil {
		t.Fatal("invalid meter should error")
	}

	badModel := valid
	badModel.Model = "not_a_model"
	if err := validateCatalogPricingRule(badModel, "USD"); err == nil {
		t.Fatal("invalid model should error")
	}

	badPrice := valid
	badPrice.UnitPrice = "abc"
	if err := validateCatalogPricingRule(badPrice, "USD"); err == nil {
		t.Fatal("non-numeric unit_price should error")
	}
}

func TestAliasFromRef(t *testing.T) {
	if got, err := aliasFromRef("quartermaster.system_tenant"); err != nil || got != "frameworks" {
		t.Fatalf("system_tenant -> (frameworks,nil), got (%q,%v)", got, err)
	}
	if got, err := aliasFromRef("quartermaster.tenants[acme]"); err != nil || got != "acme" {
		t.Fatalf("tenants[acme] -> (acme,nil), got (%q,%v)", got, err)
	}
	if _, err := aliasFromRef("garbage"); err == nil {
		t.Fatal("malformed ref should error")
	}
}

func TestPickPrimary(t *testing.T) {
	if pickPrimary(nil) != "" {
		t.Fatal("no clusters -> empty primary")
	}
	// Rows are pre-sorted by required_tier_level DESC, so the first is primary.
	got := pickPrimary([]eligibleCluster{{ID: "c-best"}, {ID: "c-other"}})
	if got != "c-best" {
		t.Fatalf("primary = %q, want c-best", got)
	}
}
