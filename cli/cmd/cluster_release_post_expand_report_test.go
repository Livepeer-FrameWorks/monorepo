package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	"github.com/spf13/cobra"
)

var errPostExpandHarnessStoppedAtUpgrades = errors.New("harness: stopped at service upgrades")

// installFakeSQLClient puts a `psql` on PATH that logs every SQL document it is
// fed and answers the schema probe with probe and anything else with report.
// The real report script then runs locally against it.
func installFakeSQLClient(t *testing.T, probe, report string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sql.log")
	script := "#!/bin/sh\ninput=\"$(cat)\"\nprintf '%s\\n----\\n' \"$input\" >> '" + logPath + "'\n" +
		"case \"$input\" in\n  *information_schema*) printf '%s\\n' '" + probe + "' ;;\n  *) cat <<'REPORT_EOF'\n" + report + "\nREPORT_EOF\n  ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "psql"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func readSQLLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// postExpandReportManifest places the Quartermaster database on a local host,
// so the report script runs through the local runner.
func postExpandReportManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{"db-1": {Name: "db-1", ExternalIP: "127.0.0.1", User: "root"}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled:   true,
				Host:      "db-1",
				Password:  "harness",
				Databases: []inventory.DatabaseConfig{{Name: "commodore"}, {Name: "quartermaster"}},
			},
		},
	}
}

// applyReleaseForPostExpandReport drives runReleaseApply with the prior-release
// preflight faked, records the migrate and upgrade steps, and stops at the
// service upgrades.
func applyReleaseForPostExpandReport(t *testing.T, dryRun bool, sqlLog string) (string, []string, error) {
	t.Helper()
	h := &releaseApplyHarness{}
	h.install(t)
	origMigrate, origUpgrades := releaseRunMigrateFn, releaseRunUpgradesFn
	t.Cleanup(func() { releaseRunMigrateFn, releaseRunUpgradesFn = origMigrate, origUpgrades })

	var steps []string
	releaseRunMigrateFn = func(_ *cobra.Command, _ *resolvedCluster, gotDryRun bool, phase string, _ bool, _ string, _, _ bool) error {
		if gotDryRun != dryRun {
			t.Errorf("migrate %s dry-run = %v, want %v", phase, gotDryRun, dryRun)
		}
		if phase == "expand" && readSQLLog(t, sqlLog) != "" {
			t.Errorf("the post-expand report queried Quartermaster before expand ran")
		}
		steps = append(steps, "migrate:"+phase)
		return nil
	}
	releaseRunUpgradesFn = func(*cobra.Command, *resolvedCluster, *reconcileEnv, []ReleaseTransition, []string, string, releaseApplyOptions) (map[string]struct{}, error) {
		if readSQLLog(t, sqlLog) != "" {
			steps = append(steps, "report")
		}
		steps = append(steps, "upgrades")
		return nil, errPostExpandHarnessStoppedAtUpgrades
	}

	cmd := newClusterReleaseApplyCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set(unsafeCLIFloorFlag, "true"); err != nil {
		t.Fatal(err)
	}
	err := runReleaseApply(cmd, &resolvedCluster{Manifest: postExpandReportManifest()}, releaseApplyOptions{version: "v0.3.11", dryRun: dryRun, yes: true})
	return out.String(), steps, err
}

func TestReleaseApplyPrintsPostExpandReportBetweenExpandAndUpgrades(t *testing.T) {
	report := `{"expand_applied":true,` +
		`"managed_keyless_total":2,"managed_keyless":[{"node_id":"edge-managed-1","cluster_id":"eu-1","tenant_id":"t-1","enrollment_origin":"gitops_seed","last_seen":"2026-09-20T10:00:00"}],` +
		`"customer_keyless_total":1,"customer_keyless":[{"node_id":"edge-customer-1","cluster_id":"private-1","tenant_id":"t-2","enrollment_origin":"runtime_enrolled","last_seen":null}],` +
		`"dns_grants_total":1,"dns_grants":[{"tenant_id":"t-3","name":"Acme","subdomain":"acme","custom_domain":null,"custom_subdomain_enabled":true,"custom_domain_enabled":false}],` +
		`"dns_handoff_present":false}`
	sqlLog := installFakeSQLClient(t, "t", report)

	out, steps, err := applyReleaseForPostExpandReport(t, false, sqlLog)
	if !errors.Is(err, errPostExpandHarnessStoppedAtUpgrades) {
		t.Fatalf("err = %v, want the harness to stop at the service upgrades\n%s", err, out)
	}
	if got := strings.Join(steps, " "); got != "migrate:expand report upgrades" {
		t.Fatalf("steps = %q, want the report between expand and the service upgrades", got)
	}

	queries := readSQLLog(t, sqlLog)
	if n := strings.Count(queries, "BEGIN READ ONLY;"); n != 2 {
		t.Fatalf("SQL documents opening BEGIN READ ONLY = %d, want 2 (probe + report):\n%s", n, queries)
	}
	if !strings.Contains(queries, nodeidentity.ManagedKeylessNodesSQL) {
		t.Fatalf("report does not use the verifier's managed-keyless query:\n%s", queries)
	}

	expand, section, upgrades := strings.Index(out, "[2/4]"), strings.Index(out, "Post-expand report"), strings.Index(out, "[3/4]")
	if expand < 0 || section < expand || upgrades < section {
		t.Fatalf("report must print between [2/4] and [3/4]:\n%s", out)
	}
	printed := out[section:upgrades]
	for _, want := range []string{
		"Operator-managed nodes without an identity key: 2",
		"edge-managed-1  cluster=eu-1  tenant=t-1  origin=gitops_seed  last seen=2026-09-20T10:00:00",
		"(showing 1 of 2)",
		"Customer-enrolled nodes without an identity key: 1",
		"edge-customer-1  cluster=private-1  tenant=t-2  origin=runtime_enrolled  last seen=never",
		"--kind edge_node --tenant-id <tenant> --cluster-id <cluster>",
		"HELMSMAN_ROTATE_NODE_IDENTITY=true",
		"DNS grants quartermaster_tenant_dns_entitlements_v0_3_0 will clear: 1",
		"tenant t-3 (Acme)  custom subdomain=on (acme)  custom domain=off",
		`"purser_dns_entitlements_v0_3_0": not recorded yet`,
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("report missing %q:\n%s", want, printed)
		}
	}
}

func TestReleaseApplyDryRunReportsNotRunBeforeExpand(t *testing.T) {
	sqlLog := installFakeSQLClient(t, "f", `{"expand_applied":true,"managed_keyless_total":0,"customer_keyless_total":0,"dns_grants_total":0}`)

	out, steps, err := applyReleaseForPostExpandReport(t, true, sqlLog)
	if !errors.Is(err, errPostExpandHarnessStoppedAtUpgrades) {
		t.Fatalf("err = %v, want the harness to stop at the service upgrades\n%s", err, out)
	}
	if got := strings.Join(steps, " "); got != "migrate:expand report upgrades" {
		t.Fatalf("steps = %q, want the schema probe between expand and the service upgrades", got)
	}
	if !strings.Contains(out, "post-expand report: not run (expand not applied)") {
		t.Fatalf("dry-run before expand must say the report did not run:\n%s", out)
	}
	if strings.Contains(out, "nothing pending") {
		t.Fatalf("dry-run before expand printed an all-clear:\n%s", out)
	}
	if queries := readSQLLog(t, sqlLog); strings.Count(queries, "----") != 1 || !strings.Contains(queries, "information_schema") {
		t.Fatalf("only the schema probe may run when expand is not applied:\n%s", queries)
	}
}

func TestPostExpandReportPrintsNothingPendingPastTheMigrations(t *testing.T) {
	var out strings.Builder
	writePostExpandReport(&out, postExpandReport{ExpandApplied: true, DNSHandoffPresent: true})
	if got := strings.TrimSpace(out.String()); got != "post-expand report: nothing pending (no keyless nodes, no DNS grants to clear)" {
		t.Fatalf("report = %q", got)
	}
}

func TestPostExpandReportNeverFailsTheRelease(t *testing.T) {
	sqlLog := installFakeSQLClient(t, "t", "ERROR:  permission denied for schema quartermaster")
	out, steps, err := applyReleaseForPostExpandReport(t, false, sqlLog)
	if !errors.Is(err, errPostExpandHarnessStoppedAtUpgrades) {
		t.Fatalf("a failed report must not stop the release; err = %v\n%s", err, out)
	}
	if got := strings.Join(steps, " "); got != "migrate:expand report upgrades" {
		t.Fatalf("steps = %q", got)
	}
	if !strings.Contains(out, "post-expand report: not run (unexpected report output") || strings.Contains(out, "nothing pending") {
		t.Fatalf("a failed report must say it did not run:\n%s", out)
	}
}
