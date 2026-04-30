package bootstrap

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"frameworks/pkg/mist"

	"github.com/DATA-DOG/go-sqlmock"
)

// twoTierFixture is a minimal catalog for tests — full enough to exercise both
// entitlements and pricing rules without dragging the full production catalog
// into every fixture.
func twoTierFixture() []CatalogTier {
	return []CatalogTier{
		{
			TierName:        "payg",
			DisplayName:     "Pay As You Go",
			Description:     "Prepaid pay-as-you-go.",
			BasePrice:       0,
			Currency:        "EUR",
			BillingPeriod:   "monthly",
			Features:        map[string]any{"recording": true, "support_level": "community"},
			SupportLevel:    "community",
			SLALevel:        "none",
			MeteringEnabled: true,
			Entitlements:    map[string]any{},
			PricingRules: []CatalogPricingRule{
				{Meter: "delivered_minutes", Model: "tiered_graduated", UnitPrice: "0.00055"},
			},
			TierLevel:        0,
			IsDefaultPrepaid: true,
			ProcessesLive:    `[{"process":"AV"}]`,
			ProcessesVOD:     `[{"process":"Thumbs"}]`,
		},
		{
			TierName:          "free",
			DisplayName:       "Free",
			Description:       "Self-hosted, no SLA.",
			BasePrice:         0,
			Currency:          "EUR",
			BillingPeriod:     "monthly",
			Features:          map[string]any{"recording": false, "support_level": "community"},
			SupportLevel:      "community",
			SLALevel:          "none",
			MeteringEnabled:   false,
			Entitlements:      map[string]any{"recording_retention_days": 7},
			PricingRules:      nil,
			TierLevel:         1,
			IsDefaultPostpaid: true,
			ProcessesLive:     `[{"process":"AV"}]`,
			ProcessesVOD:      `[{"process":"Thumbs"}]`,
		},
	}
}

func TestEmbeddedCatalogParses(t *testing.T) {
	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatalf("EmbeddedTiers: %v", err)
	}
	wantNames := []string{"payg", "free", "supporter", "developer", "production", "enterprise"}
	if got := len(tiers); got != len(wantNames) {
		t.Fatalf("expected %d tiers in catalog, got %d", len(wantNames), got)
	}
	have := map[string]bool{}
	for _, tier := range tiers {
		have[tier.TierName] = true
	}
	for _, n := range wantNames {
		if !have[n] {
			t.Errorf("expected tier %q in embedded catalog", n)
		}
	}

	for _, tier := range tiers {
		if tier.ProcessesLive == "" || tier.ProcessesVOD == "" {
			t.Errorf("tier %q missing MistServer process json", tier.TierName)
		}
	}
}

func TestEmbeddedCatalogShape(t *testing.T) {
	// Spot-check the entitlements/pricing-rule split is preserved in the YAML.
	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatalf("EmbeddedTiers: %v", err)
	}
	byName := map[string]CatalogTier{}
	for _, tier := range tiers {
		byName[tier.TierName] = tier
	}
	supporter, ok := byName["supporter"]
	if !ok {
		t.Fatal("supporter tier missing")
	}
	if got := supporter.Entitlements["recording_retention_days"]; got == nil {
		t.Errorf("supporter missing recording_retention_days entitlement")
	}
	if len(supporter.PricingRules) == 0 {
		t.Error("supporter has no pricing rules")
	}
	for _, rule := range supporter.PricingRules {
		if rule.Meter == "average_storage_gb" && rule.Model != "all_usage" {
			t.Errorf("supporter storage rule model = %q, want all_usage (no included GB)", rule.Model)
		}
		if rule.Meter == "ai_gpu_hours" && rule.IncludedQuantity != 10 {
			t.Errorf("supporter GPU included = %v, want 10", rule.IncludedQuantity)
		}
	}
}

func TestEmbeddedCatalogMistProcessShapes(t *testing.T) {
	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatalf("EmbeddedTiers: %v", err)
	}
	for _, tier := range tiers {
		if tier.ProcessesLive != "" {
			if err := mist.ValidateProcessConfigShape(tier.ProcessesLive); err != nil {
				t.Fatalf("%s processes_live: %v", tier.TierName, err)
			}
		}
		if tier.ProcessesVOD != "" {
			if err := mist.ValidateProcessConfigShape(tier.ProcessesVOD); err != nil {
				t.Fatalf("%s processes_vod: %v", tier.TierName, err)
			}
		}
	}
}

func TestReconcileBillingTierCatalogRejectsEmpty(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	if _, err := ReconcileBillingTierCatalog(context.Background(), db, nil); err == nil {
		t.Fatal("expected error on empty tier slice")
	}
}

func TestReconcileBillingTierCatalogRejectsNilDB(t *testing.T) {
	if _, err := ReconcileBillingTierCatalog(context.Background(), nil, twoTierFixture()); err == nil {
		t.Fatal("expected error on nil db")
	}
}

func TestReconcileBillingTierCatalogRejectsDuplicatePricingMeters(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tiers := twoTierFixture()
	tiers[0].PricingRules = append(tiers[0].PricingRules, CatalogPricingRule{
		Meter:     "delivered_minutes",
		Model:     "all_usage",
		UnitPrice: "0.001",
	})
	if _, err := ReconcileBillingTierCatalog(context.Background(), db, tiers); err == nil {
		t.Fatal("expected duplicate pricing meter error")
	}
}

// TestReconcileBillingTierCatalogCreatesNewTiers covers the cold-start path: every
// tier is missing, every probe returns ErrNoRows, every row is inserted.
func TestReconcileBillingTierCatalogCreatesNewTiers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tiers := twoTierFixture()
	for i, tier := range tiers {
		fakeID := []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"}[i]

		mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM purser.billing_tiers WHERE tier_name = $1`)).
			WithArgs(tier.TierName).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`INSERT INTO purser\.billing_tiers`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(fakeID))

		// Entitlements: SELECT current → empty; INSERT each desired; no DELETE.
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT key, value::text FROM purser.tier_entitlements WHERE tier_id = $1`)).
			WithArgs(fakeID).
			WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
		for range tier.Entitlements {
			mock.ExpectExec(`INSERT INTO purser\.tier_entitlements`).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}

		// Pricing rules: SELECT current → empty; INSERT each desired.
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT meter, model, currency, included_quantity::text, unit_price::text, config::text`)).
			WithArgs(fakeID).
			WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
		for range tier.PricingRules {
			mock.ExpectExec(`INSERT INTO purser\.tier_pricing_rules`).
				WillReturnResult(sqlmock.NewResult(0, 1))
		}
	}

	res, err := ReconcileBillingTierCatalog(context.Background(), db, tiers)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Created); got != 2 {
		t.Fatalf("expected 2 created; got %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestReconcileBillingTierCatalogRollsBackOnError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM purser.billing_tiers WHERE tier_name = $1`)).
		WithArgs("payg").
		WillReturnError(sql.ErrConnDone)

	if _, err := ReconcileBillingTierCatalog(context.Background(), db, twoTierFixture()); err == nil {
		t.Fatal("expected error from probe failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
