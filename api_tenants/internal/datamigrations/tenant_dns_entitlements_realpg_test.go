//go:build schema_verify

package datamigrations

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestTenantDNSEntitlementsRunAndVerifyRealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-tenant-dns-entitlements-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenants
			(id, name, deployment_tier, custom_subdomain_enabled, custom_domain_enabled, billing_entitlements_observed_at)
		VALUES
			('11111111-1111-4111-8111-111111111131', 'Legacy paid', 'production', false, false, 'epoch'),
			('11111111-1111-4111-8111-111111111132', 'Legacy override', 'production', false, false, 'epoch'),
			('11111111-1111-4111-8111-111111111133', 'Legacy free', 'free', false, false, 'epoch')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := runTenantDNSEntitlements(ctx, db, datamigrate.RunOptions{BatchSize: 2}); err == nil {
		t.Fatal("closing migration must refuse to run before Purser publishes its sweep receipt")
	}
	var epochUnchanged int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM quartermaster.tenants
		WHERE id IN (
			'11111111-1111-4111-8111-111111111131',
			'11111111-1111-4111-8111-111111111132'
		) AND billing_entitlements_observed_at = 'epoch'::timestamptz
	`).Scan(&epochUnchanged); err != nil {
		t.Fatal(err)
	}
	if epochUnchanged != 2 {
		t.Fatalf("unsafe migration changed rows before handoff: %d remained", epochUnchanged)
	}
	// This is the production-reachable order: Purser materializes the paid
	// subscriptions first, then its full successful sweep records the receipt.
	if _, err := db.ExecContext(ctx, `
		UPDATE quartermaster.tenants
		SET custom_subdomain_enabled = (id <> '11111111-1111-4111-8111-111111111133'),
		    custom_domain_enabled = (id = '11111111-1111-4111-8111-111111111131'),
		    billing_entitlements_observed_at = NOW()
		WHERE id IN (
			'11111111-1111-4111-8111-111111111131',
			'11111111-1111-4111-8111-111111111132',
			'11111111-1111-4111-8111-111111111133'
		)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.billing_entitlement_handoffs (handoff_key, subscription_count)
		VALUES ($1, 3)
	`, tenantDNSEntitlementHandoffKey); err != nil {
		t.Fatal(err)
	}
	// A tenant created after the immutable receipt must not invalidate it.
	// Current create paths start fail-closed and already observed.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.tenants
			(id, name, deployment_tier, custom_subdomain_enabled, custom_domain_enabled, billing_entitlements_observed_at)
		VALUES ('11111111-1111-4111-8111-111111111134', 'Post receipt', 'free', false, false, NOW())
	`); err != nil {
		t.Fatal(err)
	}
	for {
		progress, err := runTenantDNSEntitlements(ctx, db, datamigrate.RunOptions{BatchSize: 2})
		if err != nil {
			t.Fatal(err)
		}
		if progress.Done {
			break
		}
	}
	if err := verifyTenantDNSEntitlements(ctx, db); err != nil {
		t.Fatal(err)
	}
	var invalid int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM quartermaster.tenants
		WHERE id IN (
			'11111111-1111-4111-8111-111111111131',
			'11111111-1111-4111-8111-111111111132',
			'11111111-1111-4111-8111-111111111133'
		) AND billing_entitlements_observed_at = 'epoch'::timestamptz
	`).Scan(&invalid); err != nil {
		t.Fatal(err)
	}
	if invalid != 0 {
		t.Fatalf("closing migration left %d unobserved entitlement rows", invalid)
	}
	var paidGrants int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM quartermaster.tenants
		WHERE id IN (
			'11111111-1111-4111-8111-111111111131',
			'11111111-1111-4111-8111-111111111132'
		) AND custom_subdomain_enabled
	`).Scan(&paidGrants); err != nil {
		t.Fatal(err)
	}
	if paidGrants != 2 {
		t.Fatalf("closing migration destroyed %d paid DNS grants", 2-paidGrants)
	}
}
