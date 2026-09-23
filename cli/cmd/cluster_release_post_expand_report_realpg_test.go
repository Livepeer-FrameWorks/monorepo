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
	"github.com/spf13/cobra"
)

// TestPostExpandReportAgainstQuartermasterRealPG runs the release report's real
// shell script and SQL against the current Quartermaster baseline. The `psql`
// the script finds on PATH forwards into the fixture container.
func TestPostExpandReportAgainstQuartermasterRealPG(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-cli-post-expand-report-realpg-%d", time.Now().UnixNano())
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
	if _, err := admin.Exec(`CREATE DATABASE quartermaster`); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/quartermaster?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	wrapper := "#!/bin/sh\nexec " + ssh.ShellQuote(docker) + " exec -i " + ssh.ShellQuote(name) + " psql \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "psql"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := context.Background()
	mustExec := func(query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(`
		INSERT INTO quartermaster.tenants
			(id, name, subdomain, custom_domain, custom_subdomain_enabled, custom_domain_enabled, billing_entitlements_observed_at)
		VALUES
			('11111111-1111-4111-8111-111111111101', 'Epoch subdomain', 'epoch-sub', NULL, true, false, 'epoch'),
			('11111111-1111-4111-8111-111111111102', 'Epoch domain', NULL, 'video.example.com', false, true, 'epoch'),
			('11111111-1111-4111-8111-111111111103', 'Epoch without grants', NULL, NULL, false, false, 'epoch'),
			('11111111-1111-4111-8111-111111111104', 'Observed grant', 'observed', NULL, true, false, NOW());

		INSERT INTO quartermaster.infrastructure_clusters
			(cluster_id, cluster_name, cluster_type, base_url, is_active)
		VALUES
			('active-cell', 'Active cell', 'edge', 'https://active.invalid', true),
			('inactive-cell', 'Inactive cell', 'edge', 'https://inactive.invalid', false);

		INSERT INTO quartermaster.infrastructure_nodes
			(node_id, cluster_id, node_name, node_type, status, enrollment_origin, last_heartbeat)
		VALUES
			('managed-gitops', 'active-cell', 'Managed GitOps', 'edge', 'active', 'gitops_seed', '2026-09-20 10:00:00'),
			('managed-adopted', 'active-cell', 'Managed adopted', 'edge', 'active', 'adopted_local', NULL),
			('self-hosted', 'active-cell', 'Self hosted', 'edge', 'active', 'runtime_enrolled', '2026-09-21 08:30:00'),
			('self-hosted-offline', 'active-cell', 'Self hosted offline', 'edge', 'offline', 'runtime_enrolled', NULL),
			('managed-keyed', 'active-cell', 'Managed keyed', 'edge', 'active', 'gitops_seed', NULL),
			('managed-offline', 'active-cell', 'Managed offline', 'edge', 'offline', 'gitops_seed', NULL),
			('managed-inactive-cell', 'inactive-cell', 'Managed inactive cell', 'edge', 'active', 'gitops_seed', NULL);

		INSERT INTO quartermaster.node_fingerprints
			(node_id, tenant_id, node_identity_public_key_ed25519, last_seen)
		VALUES
			('managed-gitops', '11111111-1111-4111-8111-111111111101', NULL, '2026-09-19 10:00:00'),
			('managed-adopted', NULL, NULL, NULL),
			('self-hosted', '11111111-1111-4111-8111-111111111102', NULL, '2026-09-01 00:00:00'),
			('self-hosted-offline', NULL, NULL, NULL),
			('managed-keyed', NULL, decode(repeat('ab', 32), 'hex'), NULL),
			('managed-offline', NULL, NULL, NULL),
			('managed-inactive-cell', NULL, NULL, NULL)
	`)

	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"db-1": {Name: "db-1", ExternalIP: "127.0.0.1", User: "root"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled:   true,
				Host:      "db-1",
				Port:      5432,
				Password:  "harness",
				Databases: []inventory.DatabaseConfig{{Name: "quartermaster"}},
			},
		},
	}
	pool := ssh.NewPool(10*time.Second, "")
	t.Cleanup(func() { pool.Close() })
	report := func() string {
		t.Helper()
		cmd := &cobra.Command{}
		var out strings.Builder
		cmd.SetOut(&out)
		runReleasePostExpandReport(ctx, cmd, &resolvedCluster{Manifest: manifest}, pool)
		return out.String()
	}
	requireLines := func(out string, want ...string) {
		t.Helper()
		for _, line := range want {
			if !strings.Contains(out, line) {
				t.Fatalf("report missing %q:\n%s", line, out)
			}
		}
	}

	out := report()
	requireLines(out,
		"Operator-managed nodes without an identity key: 2",
		"- managed-adopted  cluster=active-cell  tenant=-  origin=adopted_local  last seen=never",
		"- managed-gitops  cluster=active-cell  tenant=11111111-1111-4111-8111-111111111101  origin=gitops_seed  last seen=2026-09-20T10:00:00",
		"Customer-enrolled nodes without an identity key: 1",
		"- self-hosted  cluster=active-cell  tenant=11111111-1111-4111-8111-111111111102  origin=runtime_enrolled  last seen=2026-09-21T08:30:00",
		"DNS grants quartermaster_tenant_dns_entitlements_v0_3_0 will clear: 2",
		"tenant 11111111-1111-4111-8111-111111111101 (Epoch subdomain)  custom subdomain=on (epoch-sub)  custom domain=off",
		"tenant 11111111-1111-4111-8111-111111111102 (Epoch domain)  custom subdomain=off  custom domain=on (video.example.com)",
		`"purser_dns_entitlements_v0_3_0": not recorded yet`,
	)
	for _, excluded := range []string{"managed-keyed", "managed-offline", "managed-inactive-cell", "self-hosted-offline", "Epoch without grants", "Observed grant"} {
		if strings.Contains(out, excluded) {
			t.Fatalf("report lists %q, which neither gate would act on:\n%s", excluded, out)
		}
	}

	mustExec(`
		INSERT INTO quartermaster.billing_entitlement_handoffs (handoff_key, subscription_count)
		VALUES ('purser_dns_entitlements_v0_3_0', 3);
		INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, status, enrollment_origin)
		SELECT 'bulk-' || lpad(n::text, 3, '0'), 'active-cell', 'Bulk', 'edge', 'active', 'gitops_seed'
		FROM generate_series(1, 55) n;
		INSERT INTO quartermaster.node_fingerprints (node_id)
		SELECT 'bulk-' || lpad(n::text, 3, '0') FROM generate_series(1, 55) n
	`)
	out = report()
	requireLines(out,
		"Operator-managed nodes without an identity key: 57",
		"(showing 50 of 57)",
		`"purser_dns_entitlements_v0_3_0": recorded`,
	)
	if got := strings.Count(out, "origin=gitops_seed") + strings.Count(out, "origin=adopted_local"); got != postExpandReportRowLimit {
		t.Fatalf("listed %d managed rows, want the %d-row cap:\n%s", got, postExpandReportRowLimit, out)
	}

	mustExec(`
		UPDATE quartermaster.node_fingerprints SET node_identity_public_key_ed25519 = decode(repeat('cd', 32), 'hex')
		WHERE node_identity_public_key_ed25519 IS NULL;
		UPDATE quartermaster.tenants SET custom_subdomain_enabled = false, custom_domain_enabled = false, billing_entitlements_observed_at = NOW()
		WHERE billing_entitlements_observed_at = 'epoch'
	`)
	if out := report(); !strings.Contains(out, "post-expand report: nothing pending") {
		t.Fatalf("a cluster past both migrations must report nothing pending:\n%s", out)
	}

	mustExec(`ALTER TABLE quartermaster.node_fingerprints DROP COLUMN node_identity_public_key_ed25519`)
	out = report()
	if !strings.Contains(out, "post-expand report: not run (expand not applied)") || strings.Contains(out, "nothing pending") {
		t.Fatalf("a database without the expand columns must report not run:\n%s", out)
	}
}
