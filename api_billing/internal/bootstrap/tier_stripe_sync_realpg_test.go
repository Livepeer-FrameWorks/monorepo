//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestTierStripeSyncRepository_RealPG(t *testing.T) {
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	paidID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (
		    id, tier_name, display_name, description, base_price, currency, is_active
		) VALUES
		    ($1, 'paid-contract', 'Paid contract', NULL, 29.95, 'EUR', NULL),
		    ($2, 'free-contract', 'Free contract', 'free', 0, 'EUR', true),
		    ($3, 'inactive-contract', 'Inactive contract', 'inactive', 10, 'EUR', false)
	`, paidID, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}

	queries := purserdb.New(db)
	tiers, err := queries.ListActivePaidBillingTiersForStripe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tiers) != 1 || tiers[0].ID != paidID || tiers[0].Description != "" || tiers[0].BasePrice != "29.95" {
		t.Fatalf("Stripe tier candidates = %+v", tiers)
	}
	rows, err := queries.UpdateBillingTierStripeIDs(ctx, purserdb.UpdateBillingTierStripeIDsParams{
		StripeProductID:      sql.NullString{String: "prod_contract", Valid: true},
		StripePriceIDMonthly: sql.NullString{String: "price_contract", Valid: true},
		ID:                   paidID,
	})
	if err != nil || rows != 1 {
		t.Fatalf("update Stripe IDs rows/error = %d/%v", rows, err)
	}

	var productID, priceID string
	if err := db.QueryRowContext(ctx, `
		SELECT stripe_product_id, stripe_price_id_monthly
		FROM purser.billing_tiers WHERE id = $1
	`, paidID).Scan(&productID, &priceID); err != nil {
		t.Fatal(err)
	}
	if productID != "prod_contract" || priceID != "price_contract" {
		t.Fatalf("stored Stripe IDs = %q/%q", productID, priceID)
	}
}
