//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"testing"
)

func TestBootstrapTierCatalogRepository_RealPG(t *testing.T) { //nolint:funlen // One engine lifecycle proves the complete tier-catalog boundary.
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	tier := CatalogTier{
		TierName: "tier-contract", DisplayName: "Tier contract", Currency: "EUR",
		SupportLevel: "community", SLALevel: "none",
		Entitlements: map[string]any{"recording_retention_days": 30},
		PricingRules: []CatalogPricingRule{{
			Meter: "delivered_minutes", Model: "tiered_graduated", IncludedQuantity: 100,
			UnitPrice: "0.000550000", Config: map[string]any{"rounding": "exact"},
		}},
	}

	created := reconcileTierCatalogRealPG(t, db, tier)
	if len(created.Created) != 1 {
		t.Fatalf("first reconcile = %+v", created)
	}
	noop := reconcileTierCatalogRealPG(t, db, tier)
	if len(noop.Noop) != 1 {
		t.Fatalf("replay reconcile = %+v", noop)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE purser.billing_tiers
		SET description = NULL, support_level = NULL, sla_level = NULL,
		    metering_enabled = NULL, tier_level = NULL, is_enterprise = NULL,
		    is_default_prepaid = NULL, is_default_postpaid = NULL,
		    processes_live = NULL, processes_dvr = NULL, processes_clip = NULL,
		    processes_dvr_finalize = NULL, processes_vod = NULL,
		    stripe_product_id = 'prod_contract',
		    stripe_price_id_monthly = 'price_contract_monthly'
		WHERE tier_name = $1
	`, tier.TierName); err != nil {
		t.Fatal(err)
	}
	noop = reconcileTierCatalogRealPG(t, db, tier)
	if len(noop.Noop) != 1 {
		t.Fatalf("normalized legacy NULL replay = %+v", noop)
	}

	var tierID string
	if err := db.QueryRowContext(ctx, `
		SELECT id::text FROM purser.billing_tiers WHERE tier_name = $1
	`, tier.TierName).Scan(&tierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_entitlements (tier_id, key, value)
		VALUES ($1, 'stale_entitlement', 'true'::jsonb)
	`, tierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_pricing_rules
		    (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.1, '{}'::jsonb)
	`, tierID); err != nil {
		t.Fatal(err)
	}

	tier.DisplayName = "Tier contract updated"
	tier.Entitlements = map[string]any{"max_concurrent_streams": 8}
	tier.PricingRules[0].IncludedQuantity = 250
	tier.PricingRules[0].UnitPrice = "0.000700000"
	tier.PricingRules[0].Config = map[string]any{"rounding": "up"}
	updated := reconcileTierCatalogRealPG(t, db, tier)
	if len(updated.Updated) != 1 {
		t.Fatalf("drift reconcile = %+v", updated)
	}

	var displayName, productID, priceID string
	if err := db.QueryRowContext(ctx, `
		SELECT display_name, stripe_product_id, stripe_price_id_monthly
		FROM purser.billing_tiers WHERE id = $1
	`, tierID).Scan(&displayName, &productID, &priceID); err != nil {
		t.Fatal(err)
	}
	if displayName != tier.DisplayName || productID != "prod_contract" || priceID != "price_contract_monthly" {
		t.Fatalf("tier/Stripe state = %q %q %q", displayName, productID, priceID)
	}

	var entitlementKey, entitlementValue string
	if err := db.QueryRowContext(ctx, `
		SELECT key, value::text FROM purser.tier_entitlements WHERE tier_id = $1
	`, tierID).Scan(&entitlementKey, &entitlementValue); err != nil {
		t.Fatal(err)
	}
	if entitlementKey != "max_concurrent_streams" || entitlementValue != "8" {
		t.Fatalf("entitlement state = %q %q", entitlementKey, entitlementValue)
	}

	var meter, included, unitPrice, config string
	if err := db.QueryRowContext(ctx, `
		SELECT meter, included_quantity::text, unit_price::text, config::text
		FROM purser.tier_pricing_rules WHERE tier_id = $1
	`, tierID).Scan(&meter, &included, &unitPrice, &config); err != nil {
		t.Fatal(err)
	}
	if meter != "delivered_minutes" || included != "250.000000" || unitPrice != "0.000700000" || config != `{"rounding": "up"}` {
		t.Fatalf("pricing rule state = %q %q %q %q", meter, included, unitPrice, config)
	}
}

func reconcileTierCatalogRealPG(t *testing.T, db *sql.DB, tier CatalogTier) Result {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileBillingTierCatalog(ctx, tx, []CatalogTier{tier})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}
