//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

const tierFeatureFlagMigrationPath = "migrations/purser/v0.3.11/postdeploy/006_strip_unenforced_tier_flags.sql"

// An upgraded database whose tiers and subscriptions still carry the removed
// flags converges on the fresh-install catalog, refreshes media authority once
// per tenant on a changed tier, and is a no-op on replay.
func TestTierFeatureFlagRemovalConverges_RealPG(t *testing.T) { //nolint:funlen // One engine lifecycle proves strip, refresh fan-out, replay, and catalog convergence.
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()

	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatalf("EmbeddedTiers: %v", err)
	}
	reconcileTiersRealPG(t, db, tiers)

	const (
		tenantDedicatedA = "93000000-0000-4000-8000-00000000000a"
		tenantDedicatedB = "93000000-0000-4000-8000-00000000000b"
		tenantCancelled  = "93000000-0000-4000-8000-00000000000c"
		tenantFree       = "93000000-0000-4000-8000-00000000000d"
	)
	for _, sub := range []struct {
		tenant, tier, status, customFeatures string
	}{
		{tenantDedicatedA, "enterprise", "active", `{"api_access": true, "custom_branding": true, "processing_customizable": true}`},
		{tenantDedicatedB, "enterprise", "active", `{}`},
		{tenantCancelled, "enterprise", "cancelled", `{"recording": true}`},
		{tenantFree, "free", "active", `{"sla": true}`},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, custom_features)
			SELECT $1::uuid, id, $3, $4::jsonb FROM purser.billing_tiers WHERE tier_name = $2
		`, sub.tenant, sub.tier, sub.status, sub.customFeatures); err != nil {
			t.Fatalf("seed subscription %s: %v", sub.tenant, err)
		}
	}

	// The enterprise tier carries the removed flags as a pre-v0.3.11 catalog
	// wrote them; the free tier already has the current shape.
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.billing_tiers
		SET features = features || '{"recording": true, "analytics": true, "api_access": true, "custom_branding": true}'::jsonb
		WHERE tier_name = 'enterprise'
	`); err != nil {
		t.Fatalf("seed legacy tier flags: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM purser.media_authority_refresh_outbox`); err != nil {
		t.Fatalf("isolate refresh outbox: %v", err)
	}

	migration, err := dbsql.Content.ReadFile(tierFeatureFlagMigrationPath)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	assertJSONB(t, db, `SELECT features::text FROM purser.billing_tiers WHERE tier_name = 'enterprise'`,
		`{"sla": true, "support_level": "dedicated", "processing_customizable": true}`)
	assertJSONB(t, db, `SELECT custom_features::text FROM purser.tenant_subscriptions WHERE tenant_id = '`+tenantDedicatedA+`'`,
		`{"processing_customizable": true}`)
	assertJSONB(t, db, `SELECT custom_features::text FROM purser.tenant_subscriptions WHERE tenant_id = '`+tenantCancelled+`'`,
		`{}`)
	assertJSONB(t, db, `SELECT custom_features::text FROM purser.tenant_subscriptions WHERE tenant_id = '`+tenantFree+`'`,
		`{"sla": true}`)

	refreshes := refreshOutboxByTenant(t, db)
	want := map[string]string{
		tenantDedicatedA: "billing_tier_authority_changed:1",
		tenantDedicatedB: "billing_tier_authority_changed:1",
	}
	if len(refreshes) != len(want) {
		t.Fatalf("refresh outbox = %v, want %v", refreshes, want)
	}
	for tenant, row := range want {
		if refreshes[tenant] != row {
			t.Fatalf("refresh outbox = %v, want %v", refreshes, want)
		}
	}

	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("replay migration: %v", err)
	}
	if replayed := refreshOutboxByTenant(t, db); len(replayed) != len(want) ||
		replayed[tenantDedicatedA] != want[tenantDedicatedA] || replayed[tenantDedicatedB] != want[tenantDedicatedB] {
		t.Fatalf("replay changed refresh outbox: %v", replayed)
	}

	result := reconcileTiersRealPG(t, db, tiers)
	if len(result.Created) != 0 || len(result.Updated) != 0 || len(result.Noop) != len(tiers) {
		t.Fatalf("catalog reconcile after migration = %+v, want every tier noop", result)
	}
}

func reconcileTiersRealPG(t *testing.T, db *sql.DB, tiers []CatalogTier) Result {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ReconcileBillingTierCatalog(ctx, tx, tiers)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertJSONB(t *testing.T, db *sql.DB, query, want string) {
	t.Helper()
	var equal bool
	var got string
	if err := db.QueryRowContext(context.Background(),
		`SELECT got::jsonb = $1::jsonb, got FROM (`+query+`) AS q(got)`, want,
	).Scan(&equal, &got); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if !equal {
		t.Fatalf("%s = %s, want %s", query, got, want)
	}
}

// refreshOutboxByTenant maps tenant ID to "reason:revision" for every row.
func refreshOutboxByTenant(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT tenant_id::text, reason || ':' || revision::text
		FROM purser.media_authority_refresh_outbox
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var tenant, row string
		if err := rows.Scan(&tenant, &row); err != nil {
			t.Fatal(err)
		}
		if previous, ok := out[tenant]; ok {
			t.Fatalf("tenant %s has refresh rows %s and %s", tenant, previous, row)
		}
		out[tenant] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
