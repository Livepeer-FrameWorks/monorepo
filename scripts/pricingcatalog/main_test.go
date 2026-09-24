package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderCanonicalCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", catalogPath))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := render(raw)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var out output
	if err := json.Unmarshal(rendered, &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]tier{}
	for _, tr := range out.Tiers {
		byID[tr.ID] = tr
	}
	if _, ok := byID["enterprise"]; ok {
		t.Error("enterprise has contract pricing and must not be emitted")
	}
	free, ok := byID["free"]
	if !ok {
		t.Fatal("free tier missing")
	}
	if free.Storage.Included != 10 || free.Limits.StorageGiB != 10 {
		t.Errorf("free storage = %+v limits %+v, want 10 GiB-months and a 10 GiB cap", free.Storage, free.Limits)
	}
	// Storage is priced per GiB-month; a per-hour-scale price here means the
	// catalog regressed to GiB-hour units.
	for _, tr := range out.Tiers {
		if tr.Storage.UnitPrice > 1 {
			t.Errorf("tier %s storage price %v/GiB-month is implausible", tr.ID, tr.Storage.UnitPrice)
		}
	}
}

func TestStorageHoursMatchesRatingEngine(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "pkg", "billing", "units.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "const HoursPerBillingMonth = 730") || storageHoursPerMonth != 730 {
		t.Fatal("storageHoursPerMonth must equal pkg/billing.HoursPerBillingMonth")
	}
}

func TestRenderRejectsUnmodeledMeter(t *testing.T) {
	catalog := `
tiers:
  - tier_name: supporter
    display_name: Supporter
    base_price: 79
    currency: EUR
    billing_period: monthly
    pricing_rules:
      - meter: delivered_minutes
        model: tiered_graduated
        included_quantity: 1000
        unit_price: "0.001"
      - meter: storage_gb_seconds_cold
        model: all_usage
        unit_price: "0.035"
        config: { rated_quantity_divisor: 2628000, rated_unit: gibibyte_month }
      - meter: egress_gb
        model: all_usage
        unit_price: "0.01"
`
	_, err := render([]byte(catalog))
	if err == nil || !strings.Contains(err.Error(), "egress_gb") {
		t.Fatalf("render error = %v, want unmodeled egress_gb", err)
	}
}

func TestRenderRejectsStorageWithoutMonthlyUnit(t *testing.T) {
	catalog := `
tiers:
  - tier_name: supporter
    display_name: Supporter
    base_price: 79
    currency: EUR
    billing_period: monthly
    pricing_rules:
      - meter: delivered_minutes
        model: tiered_graduated
        unit_price: "0.001"
      - meter: storage_gb_seconds_cold
        model: all_usage
        unit_price: "0.035"
`
	_, err := render([]byte(catalog))
	if err == nil || !strings.Contains(err.Error(), "explicitly rate GiB-months") {
		t.Fatalf("render error = %v, want missing storage-unit error", err)
	}
}
