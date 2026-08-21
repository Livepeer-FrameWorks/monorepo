//go:build schema_verify

package grpc

import (
	"context"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestTierCatalogReads_RealPG(t *testing.T) { //nolint:funlen // One fixture proves normalized rows, children, and both keyset directions.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	const (
		firstID    = "10000000-0000-4000-8000-000000000001"
		secondID   = "20000000-0000-4000-8000-000000000002"
		inactiveID = "30000000-0000-4000-8000-000000000003"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (
		    id, tier_name, display_name, description, base_price, currency,
		    support_level, sla_level, metering_enabled, is_active, tier_level,
		    is_enterprise, processes_live, processes_dvr
		) VALUES
		    ($1, 'read-first', 'Read first', NULL, 0, 'EUR', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL),
		    ($2, 'read-second', 'Read second', 'second', 20, 'EUR', 'priority', 'gold', true, true, 2, false, '[]', '[]'),
		    ($3, 'read-inactive', 'Read inactive', 'inactive', 30, 'EUR', 'priority', 'gold', true, false, 3, false, '[]', '[]')
	`, firstID, secondID, inactiveID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_entitlements (tier_id, key, value)
		VALUES ($1, 'recording_retention_days', '30'::jsonb)
	`, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tier_pricing_rules
		    (tier_id, meter, model, currency, included_quantity, unit_price, config)
		VALUES ($1, 'delivered_minutes', 'tiered_graduated', 'EUR', 100, 0.00055, '{}'::jsonb)
	`, firstID); err != nil {
		t.Fatal(err)
	}

	queries := purserdb.New(db)
	forward, err := queries.ListBillingTiers(ctx, purserdb.ListBillingTiersParams{
		CursorID: firstID, ResultLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(forward) != 2 || forward[0].ID.String() != firstID || forward[1].ID.String() != secondID {
		t.Fatalf("active forward tiers = %+v", forward)
	}
	if forward[0].Description != "" || forward[0].SupportLevel != "community" || forward[0].SlaLevel != "none" ||
		forward[0].MeteringEnabled || !forward[0].IsActive || forward[0].TierLevel != 0 || string(forward[0].ProcessesLive) != "[]" {
		t.Fatalf("normalized legacy tier = %+v", forward[0])
	}
	afterFirst, err := queries.ListBillingTiers(ctx, purserdb.ListBillingTiersParams{
		HasCursor: true, CursorTierLevel: 0, CursorID: firstID, ResultLimit: 10,
	})
	if err != nil || len(afterFirst) != 1 || afterFirst[0].ID.String() != secondID {
		t.Fatalf("forward cursor tiers/error = %+v/%v", afterFirst, err)
	}
	backward, err := queries.ListBillingTiers(ctx, purserdb.ListBillingTiersParams{
		IncludeInactive: true, HasCursor: true, Backward: true,
		CursorTierLevel: 3, CursorID: inactiveID, ResultLimit: 10,
	})
	if err != nil || len(backward) != 2 || backward[0].ID.String() != secondID || backward[1].ID.String() != firstID {
		t.Fatalf("backward cursor tiers/error = %+v/%v", backward, err)
	}

	server := &PurserServer{db: db, logger: logging.NewLogger()}
	tier, err := server.GetBillingTier(ctx, &purserpb.GetBillingTierRequest{TierId: firstID})
	if err != nil {
		t.Fatal(err)
	}
	if tier.GetDescription() != "" || tier.GetSupportLevel() != "community" || tier.GetProcessesLive() != "[]" ||
		len(tier.GetPricingRules()) != 1 || tier.GetEntitlements()["recording_retention_days"] != "30" {
		t.Fatalf("mapped tier = %+v", tier)
	}
	meters, err := server.ListMeterDefinitions(ctx, nil)
	if err != nil || len(meters.GetMeters()) == 0 {
		t.Fatalf("meter definitions/error = %+v/%v", meters, err)
	}
}
