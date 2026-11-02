//go:build schema_verify

package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

// TestAccountRecoveryDoctorRealPG runs the doctor's script and SQL against the
// current Commodore and Purser baselines: a clean platform passes, and each
// alert condition fails the check naming its tenant.
func TestAccountRecoveryDoctorRealPG(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-cli-account-recovery-doctor-realpg-%d", time.Now().UnixNano())
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
	admin, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if err := dockerpg.WaitReady(admin, name); err != nil {
		t.Fatal(err)
	}
	open := func(database string) *sql.DB {
		t.Helper()
		if _, err := admin.Exec(`CREATE DATABASE ` + database); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/%s?sslmode=disable", port, database))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		schema, err := dbsql.Content.ReadFile("schema/" + database + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatal(err)
		}
		return db
	}
	commodore, purser := open("commodore"), open("purser")

	bin := t.TempDir()
	wrapper := "#!/bin/sh\nexec " + ssh.ShellQuote(docker) + " exec -i " + ssh.ShellQuote(name) + " psql \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"db-1": {Name: "db-1", ExternalIP: "127.0.0.1", User: "root"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true, Host: "db-1", Port: 5432, Password: "harness",
				Databases: []inventory.DatabaseConfig{{Name: "commodore"}, {Name: "purser"}},
			},
		},
	}
	pool := ssh.NewPool(10*time.Second, "")
	t.Cleanup(func() { pool.Close() })
	ctx := context.Background()

	outcome := doctorAccountRecovery(ctx, &resolvedCluster{Manifest: manifest}, pool, "")
	if !outcome.passed || outcome.result.Status != "healthy" {
		t.Fatalf("clean platform: status=%s error=%q", outcome.result.Status, outcome.result.Error)
	}

	const (
		closedTenant = "11111111-1111-4111-8111-111111111111"
		heldTenant   = "22222222-2222-4222-8222-222222222222"
		lowerTier    = "33333333-3333-4333-8333-333333333333"
		upperTier    = "44444444-4444-4444-8444-444444444444"
		// The spent tenants sort after the others so the listing order is fixed.
		spentTenant       = "55555555-5555-4555-8555-555555555555"
		unsuspendedTenant = "66666666-6666-4666-8666-666666666666"
	)
	mustExec := func(db *sql.DB, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(commodore, `INSERT INTO commodore.tenant_account_states(tenant_id, closed_at, reason, closed_by, closure_attempted_at, closure_error, closure_failures)
		VALUES ($1, NOW() - INTERVAL '1 hour', 'closed', 'operator', NOW(), 'quartermaster unavailable', 3)`, closedTenant)
	mustExec(commodore, `INSERT INTO commodore.media_authority_refresh_obligations(target_key, lane, tenant_id, target_kind, status, pending_since, last_reason, last_source_service, last_source_event_id)
		VALUES ('tenant:' || $1, 'ingest_revocation', $1::uuid, 'tenant', 'pending', NOW() - INTERVAL '2 hours', 'account_inactive', 'commodore', 'account-closed-stop:' || $1)`, closedTenant)
	mustExec(purser, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, tier_level) VALUES ($1, 'doctor-lower', 'Lower', 1), ($2, 'doctor-upper', 'Upper', 2)`, lowerTier, upperTier)
	mustExec(purser, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, pending_tier_id, pending_reason, pending_provider_tier_id, pending_effective_at)
		VALUES ($1, $2, 'active', 'postpaid', $3, 'downgrade', $3, NOW() - INTERVAL '3 days')`, heldTenant, upperTier, lowerTier)
	mustExec(purser, `INSERT INTO purser.provider_webhook_inbox(provider, event_key, headers, raw_payload, status, attempts)
		VALUES ('tier_downgrade', 'tier-downgrade:' || $1, jsonb_build_object('tenant_id', $1::text), '{}'::bytea, 'failed', 4)`, heldTenant)
	// A spent prepaid tenant only refuses new sessions; one past the -€10
	// threshold that was never suspended is a failed enforcement.
	mustExec(purser, `INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model)
		VALUES ($1, $3, 'active', 'prepaid'), ($2, $3, 'active', 'prepaid')`, spentTenant, unsuspendedTenant, lowerTier)
	mustExec(purser, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, -400, 'EUR')`, spentTenant)

	outcome = doctorAccountRecovery(ctx, &resolvedCluster{Manifest: manifest}, pool, "")
	if strings.Contains(outcome.result.Error, "suspension threshold") {
		t.Fatalf("a spent balance above -€10 judged a failed suspension: %q", outcome.result.Error)
	}
	mustExec(purser, `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, -1500, 'EUR')`, unsuspendedTenant)

	outcome = doctorAccountRecovery(ctx, &resolvedCluster{Manifest: manifest}, pool, "")
	if outcome.passed || outcome.result.Status != "unhealthy" {
		t.Fatalf("stuck platform passed: status=%s error=%q", outcome.result.Status, outcome.result.Error)
	}
	for _, want := range []string{
		"1 tenant closure(s) failing Quartermaster synchronization (" + closedTenant + ": 3 failures)",
		"1 session stop(s) pending",
		"(" + closedTenant + ": 1 stops)",
		"1 confirmed downgrade(s) held past their effective time (" + heldTenant,
		"1 billing recovery item(s) failing repeatedly or unresolved over 14 days (" + heldTenant + ": 1 items)",
		"1 prepaid tenant(s) below the -€10 suspension threshold but not suspended; their running sessions continue (" + unsuspendedTenant + ": 1500 cents owed, " + spentTenant + ": 400 cents owed)",
	} {
		if !strings.Contains(outcome.result.Error, want) {
			t.Fatalf("doctor error %q missing %q", outcome.result.Error, want)
		}
	}
	if strings.Contains(outcome.result.Error, "could not be checked") {
		t.Fatalf("doctor misread the probe: %q", outcome.result.Error)
	}
}
