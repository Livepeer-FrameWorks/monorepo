//go:build schema_verify

package tieraccess

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startTierAccessRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-tieraccess-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatalf("read Purser schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply Purser schema: %v", err)
	}
	return db
}

func TestTierAccessEligibilityQuery_RealPG(t *testing.T) {
	db := startTierAccessRealPG(t)
	ctx := context.Background()
	const tenantID = "10000000-0000-4000-8000-000000000001"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers
			(id, tier_name, display_name, tier_level)
		VALUES
			('20000000-0000-4000-8000-000000000002', 'supporter', 'Supporter', 2);
		INSERT INTO purser.tenant_subscriptions
			(id, tenant_id, tier_id, status)
		VALUES
			('30000000-0000-4000-8000-000000000003', '10000000-0000-4000-8000-000000000001', '20000000-0000-4000-8000-000000000002', 'active');
	`); err != nil {
		t.Fatalf("seed billing tier: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.cluster_pricing
			(id, cluster_id, pricing_model, required_tier_level, allow_free_tier)
		VALUES
			(gen_random_uuid(), 'official-free', 'free_unmetered', 0, true),
			(gen_random_uuid(), 'official-supporter', 'metered', 2, false),
			(gen_random_uuid(), 'official-enterprise', 'custom', 5, false),
			(gen_random_uuid(), 'not-official', 'metered', 1, false)
	`); err != nil {
		t.Fatalf("seed cluster pricing: %v", err)
	}

	qm := &fakeQM{official: []string{"official-free", "official-supporter", "official-enterprise"}, deploymentTier: "supporter"}
	reconciler := &Reconciler{db: db, qm: qm, logger: logging.NewLogger()}
	eligible, primary, err := reconciler.Reconcile(ctx, tenantID, 2, "supporter")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if strings.Join(eligible, ",") != "official-supporter,official-free" || primary != "official-supporter" {
		t.Fatalf("eligible=%v primary=%q", eligible, primary)
	}
	wantCalls := "grant:official-supporter|grant:official-free|primary:official-supporter"
	if strings.Join(qm.calls, "|") != wantCalls {
		t.Fatalf("calls=%v, want %s", qm.calls, wantCalls)
	}
}

func TestDNSEntitlementUpgradeCatalogAndSuspendedLookup_RealPG(t *testing.T) {
	db := startTierAccessRealPG(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, tier_level)
		VALUES
			('21000000-0000-4000-8000-000000000001', 'payg', 'Pay as you go', 0),
			('21000000-0000-4000-8000-000000000002', 'free', 'Free', 0),
			('21000000-0000-4000-8000-000000000003', 'supporter', 'Supporter', 1),
			('21000000-0000-4000-8000-000000000004', 'developer', 'Developer', 2),
			('21000000-0000-4000-8000-000000000005', 'production', 'Production', 3),
			('21000000-0000-4000-8000-000000000006', 'enterprise', 'Enterprise', 4)
	`); err != nil {
		t.Fatalf("seed tiers: %v", err)
	}
	migration, err := dbsql.Content.ReadFile("migrations/purser/v0.3.0/expand/024_dns_entitlement_catalog_defaults.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	var paid, free int
	if err := db.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE bt.tier_name IN ('supporter','developer','production','enterprise') AND te.value = 'true'::jsonb),
			count(*) FILTER (WHERE bt.tier_name IN ('payg','free') AND te.value = 'false'::jsonb)
		FROM purser.tier_entitlements te
		JOIN purser.billing_tiers bt ON bt.id = te.tier_id
		WHERE te.key IN ('custom_subdomain_enabled','custom_domain_enabled')
	`).Scan(&paid, &free); err != nil {
		t.Fatalf("read migrated entitlements: %v", err)
	}
	if paid != 8 || free != 4 {
		t.Fatalf("migrated entitlement counts paid=%d free=%d, want 8/4", paid, free)
	}

	const tenantID = "21000000-0000-4000-8000-000000000010"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, status)
		VALUES ('21000000-0000-4000-8000-000000000011', $1::uuid,
		        '21000000-0000-4000-8000-000000000003', 'suspended')
	`, tenantID); err != nil {
		t.Fatalf("seed suspended subscription: %v", err)
	}
	row, err := purserdb.New(db).LoadEffectiveDNSEntitlements(ctx, tenantID)
	if err != nil {
		t.Fatalf("suspended entitlement lookup must remain available for tier changes: %v", err)
	}
	if row.TierName != "supporter" || row.CustomSubdomainEnabled || row.CustomDomainEnabled {
		t.Fatalf("unexpected suspended entitlements: %+v", row)
	}
}
