package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/spf13/cobra"
)

// `release apply` reconciles the Purser tier catalog the upgraded binary
// embeds: the desired state renders before the first mutation, the plan names
// the step, and it runs after the postdeploy migrations.
func TestReleaseApplyReconcilesPurserTierCatalog(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	origAccounts := releasePrepareAccountsFn
	origMigrate, origUpgrades, origPlacements, origCatalog := releaseRunMigrateFn, releaseRunUpgradesFn, releaseReconcilePlacementsFn, releasePrepareTierCatalogFn
	t.Cleanup(func() {
		releasePrepareAccountsFn = origAccounts
		releaseRunMigrateFn, releaseRunUpgradesFn, releaseReconcilePlacementsFn, releasePrepareTierCatalogFn = origMigrate, origUpgrades, origPlacements, origCatalog
	})
	installFakeSQLClient(t, "f", `{}`)

	var steps []string
	releasePrepareAccountsFn = func(*cobra.Command, *resolvedCluster, *ssh.Pool, string, string) (func(context.Context, bool) error, error) {
		steps = append(steps, "accounts:render")
		return func(_ context.Context, dryRun bool) error {
			if !dryRun {
				t.Error("accounts reconcile unexpectedly ran without dry-run")
			}
			steps = append(steps, "accounts:reconcile")
			return nil
		}, nil
	}
	releasePrepareTierCatalogFn = func(*cobra.Command, *resolvedCluster, *ssh.Pool, string) (func(context.Context, bool) error, error) {
		steps = append(steps, "catalog:render")
		return func(_ context.Context, dryRun bool) error {
			if !dryRun {
				t.Errorf("catalog reconcile dry-run = false, want true")
			}
			steps = append(steps, "catalog:reconcile")
			return nil
		}, nil
	}
	releaseRunMigrateFn = func(_ *cobra.Command, _ *resolvedCluster, _ bool, phase string, _ bool, _ string, _, _ bool) error {
		steps = append(steps, "migrate:"+phase)
		return nil
	}
	releaseRunUpgradesFn = func(*cobra.Command, *resolvedCluster, *reconcileEnv, []ReleaseTransition, []string, string, releaseApplyOptions) (map[string]struct{}, error) {
		steps = append(steps, "upgrades")
		return map[string]struct{}{}, nil
	}
	releaseReconcilePlacementsFn = func(context.Context, *cobra.Command, *resolvedCluster, map[string]struct{}, bool) error {
		steps = append(steps, "placements")
		return nil
	}

	cmd := newClusterReleaseApplyCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set(unsafeCLIFloorFlag, "true"); err != nil {
		t.Fatal(err)
	}
	if err := runReleaseApply(cmd, &resolvedCluster{Manifest: postExpandReportManifest()}, releaseApplyOptions{version: "v0.3.11", dryRun: true, yes: true}); err != nil {
		t.Fatalf("release apply: %v\n%s", err, out.String())
	}

	want := "accounts:render catalog:render migrate:expand upgrades placements migrate:postdeploy catalog:reconcile accounts:reconcile"
	if got := strings.Join(steps, " "); got != want {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "then the Purser billing reconcile (purser bootstrap + validate)") || !strings.Contains(out.String(), "Commodore accounts reconcile") {
		t.Fatalf("release plan does not name the billing reconcile:\n%s", out.String())
	}
}

func TestPrepareReleaseTierCatalogSkipsManifestWithoutPurser(t *testing.T) {
	step, err := prepareReleaseTierCatalog(&cobra.Command{}, &resolvedCluster{Manifest: postExpandReportManifest()}, nil, "v0.3.14")
	if err != nil || step != nil {
		t.Fatalf("prepare without purser = (%v, %v), want no step", step != nil, err)
	}
}

func TestPrepareReleaseAccountsIncludesReleaseCandidate(t *testing.T) {
	for _, version := range []string{"v0.3.13", "v0.3.14-rc1", "v0.3.14"} {
		t.Run(version, func(t *testing.T) {
			rc := &resolvedCluster{
				Manifest:     &inventory.Manifest{Profile: "dev", WireGuard: &inventory.WireGuardConfig{MeshCIDR: "10.99.0.0/16"}, Services: map[string]inventory.ServiceConfig{"commodore": {Enabled: true}}},
				ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml"), SourcePersistsManifest: true,
			}
			step, err := prepareReleaseAccounts(&cobra.Command{}, rc, nil, version, "")
			if err != nil {
				t.Fatal(err)
			}
			if (step != nil) != (version != "v0.3.13") {
				t.Fatalf("accounts step for %s exists=%v", version, step != nil)
			}
			if step != nil {
				if err = step(context.Background(), true); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// stubBootstrapPlans records each bootstrap dry-run plan the release dry run asks for, as
// "<service> <args> at <target version>".
func stubBootstrapPlans(t *testing.T) *[]string {
	t.Helper()
	original := releaseRunBootstrapPlanFn
	t.Cleanup(func() { releaseRunBootstrapPlanFn = original })
	var plans []string
	releaseRunBootstrapPlanFn = func(_ context.Context, _ *cobra.Command, _ *inventory.Manifest, _ *ssh.Pool, service string, rendered []byte, args []string, target string) error {
		if len(rendered) == 0 {
			t.Fatalf("%s plan without the rendered desired state", service)
		}
		plans = append(plans, strings.TrimSpace(service+" "+strings.Join(args, " "))+" at "+target)
		return nil
	}
	return &plans
}

// The release dry run asks Purser for its own bootstrap plan with the rendered desired state, at the version the
// release installs; the real run applies instead.
func TestReleaseTierCatalogDryRunPrintsPurserPlan(t *testing.T) {
	plans := stubBootstrapPlans(t)
	original := releaseRunAccountsBootstrapFn
	t.Cleanup(func() { releaseRunAccountsBootstrapFn = original })
	rc := &resolvedCluster{Manifest: &inventory.Manifest{Profile: "dev", WireGuard: &inventory.WireGuardConfig{MeshCIDR: "10.99.0.0/16"}, Services: map[string]inventory.ServiceConfig{"purser": {Enabled: true}}}, ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml"), SourcePersistsManifest: true}
	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	step, err := prepareReleaseTierCatalog(cmd, rc, nil, "v0.3.14")
	if err != nil {
		t.Fatal(err)
	}
	if err = step(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*plans, ",") != "purser at v0.3.14" {
		t.Fatalf("plans = %v, want purser's own dry run at v0.3.14", *plans)
	}
}

func TestReleaseAccountsUsesOnlyAccountsAndShowsNames(t *testing.T) {
	original := releaseRunAccountsBootstrapFn
	t.Cleanup(func() { releaseRunAccountsBootstrapFn = original })
	plans := stubBootstrapPlans(t)
	calls := 0
	releaseRunAccountsBootstrapFn = func(_ context.Context, _ *cobra.Command, _ *inventory.Manifest, _ *ssh.Pool, service string, _ []byte, args []string) error {
		calls++
		if service != "commodore" || len(args) != 1 || args[0] != "--accounts-only" {
			t.Fatalf("bootstrap service=%q args=%v", service, args)
		}
		return nil
	}
	rc := &resolvedCluster{Manifest: &inventory.Manifest{Profile: "dev", WireGuard: &inventory.WireGuardConfig{MeshCIDR: "10.99.0.0/16"}, Services: map[string]inventory.ServiceConfig{"commodore": {Enabled: true}}}, ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml"), SourcePersistsManifest: true}
	cmd := &cobra.Command{}
	cmd.Flags().String("bootstrap-admin-email", "operator@example.test", "")
	cmd.Flags().String("bootstrap-admin-password", "must-stay-private", "")
	var out strings.Builder
	cmd.SetOut(&out)
	step, err := prepareReleaseAccounts(cmd, rc, nil, "v0.3.14-rc1", "v0.3.14-rc1")
	if err != nil {
		t.Fatal(err)
	}
	if err = step(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "operator@example.test owner") || strings.Contains(out.String(), "must-stay-private") || calls != 0 {
		t.Fatalf("dry-run output=%s calls=%d", out.String(), calls)
	}
	if strings.Join(*plans, ",") != "commodore --accounts-only at v0.3.14-rc1" {
		t.Fatalf("plans = %v, want commodore's accounts-only dry run at v0.3.14-rc1", *plans)
	}
	if err = step(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(*plans) != 1 {
		t.Fatalf("bootstrap calls=%d plans=%v; the real run applies and plans nothing", calls, *plans)
	}
}

func TestReleasePlanNamesAccountsReconcileAtCapabilityBoundary(t *testing.T) {
	harness := &releaseApplyHarness{}
	harness.install(t)
	for _, version := range []string{"v0.3.13", "v0.3.14-rc1"} {
		manifest := postExpandReportManifest()
		manifest.Profile = "dev"
		manifest.Services = map[string]inventory.ServiceConfig{"commodore": {Enabled: true, Host: "db-1"}}
		command := newClusterReleasePlanCmd()
		command.SetContext(context.Background())
		var out strings.Builder
		command.SetOut(&out)
		command.SetErr(&out)
		if err := command.Flags().Set(unsafeCLIFloorFlag, "true"); err != nil {
			t.Fatal(err)
		}
		if err := runReleasePlan(command, &resolvedCluster{Manifest: manifest}, releasePlanOptions{version: version}); err != nil {
			t.Fatalf("plan %s: %v", version, err)
		}
		if got := strings.Contains(out.String(), "Commodore accounts reconcile"); got != (version != "v0.3.13") {
			t.Fatalf("accounts plan line for %s=%v: %s", version, got, out.String())
		}
	}
}

func TestReleaseDryRunDescribesDeclaredBillingAndUsers(t *testing.T) {
	zero := "0"
	declared := bootstrap.CustomerBilling{
		Tenant: bootstrap.TenantRefAlias("frameworks-qa"), Model: "postpaid", Tier: "developer",
		EntitlementOverrides: map[string]any{"max_concurrent_viewers": 0, "max_concurrent_streams": 2},
		OperatorGrant:        &billing.OperatorGrantSpec{BaseFee: &zero, WaiveUsage: true, Until: "2026-12-01", Reason: "QA"},
	}
	want := "postpaid developer; overrides max_concurrent_streams=2,max_concurrent_viewers=0; operator grant base fee 0, usage waived, until 2026-12-01 (QA)"
	if got := describeReleaseCustomerBilling(declared); got != want {
		t.Fatalf("billing line = %q, want %q", got, want)
	}
	user := bootstrap.AccountUserRendered{
		AccountUserCommon: bootstrap.AccountUserCommon{Email: "qa@example.test", Role: "owner", PlatformOperator: true, Wallet: "0x52908400098527886E0F7030069857D2E4169EE7"},
		Password:          "must-stay-private",
	}
	if got := describeReleaseAccountUser(user); got != "qa@example.test owner, platform operator, wallet 0x52908400098527886E0F7030069857D2E4169EE7" {
		t.Fatalf("user line = %q", got)
	}
}
