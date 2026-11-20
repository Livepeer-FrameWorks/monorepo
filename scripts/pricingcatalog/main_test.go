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
	// Plan cards quote the live rewind window from this field.
	for id, wantSeconds := range map[string]float64{"payg": 86400, "supporter": 21600, "developer": 43200, "production": 86400} {
		if got := byID[id].Limits.DvrMaxWindowSeconds; got != wantSeconds {
			t.Errorf("%s dvrMaxWindowSeconds = %v, want %v", id, got, wantSeconds)
		}
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

func TestPlanCardsUseTheAllowedLadderCeiling(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", catalogPath))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := render(raw)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Tiers []map[string]any `json:"tiers"`
	}
	if err := json.Unmarshal(rendered, &document); err != nil {
		t.Fatal(err)
	}
	for _, tier := range document.Tiers {
		want := float64(2160)
		if tier["id"] == "free" {
			want = 1080
		}
		if tier["maxVideoHeight"] != want {
			t.Fatalf("%s maxVideoHeight=%v, want %v", tier["id"], tier["maxVideoHeight"], want)
		}
	}
}

func TestPublishedPAYGDVRIsAtLeastEverySelfServeTier(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "website_marketing", "src", "data", "pricing-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog output
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	var payg float64
	for _, tier := range catalog.Tiers {
		if tier.ID == "payg" {
			payg = tier.Limits.DvrMaxWindowSeconds
		}
	}
	if payg == 0 {
		t.Fatal("published catalog has no PAYG DVR window")
	}
	for _, tier := range catalog.Tiers {
		if payg < tier.Limits.DvrMaxWindowSeconds {
			t.Errorf("published PAYG DVR maximum=%v is lower than %s=%v", payg, tier.ID, tier.Limits.DvrMaxWindowSeconds)
		}
	}
}
