package cmd

import (
	"context"
	"strings"
	"testing"

	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// `release apply` reconciles the Purser tier catalog the upgraded binary
// embeds: the desired state renders before the first mutation, the plan names
// the step, and it runs after the postdeploy migrations.
func TestReleaseApplyReconcilesPurserTierCatalog(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	origMigrate, origUpgrades, origPlacements, origCatalog := releaseRunMigrateFn, releaseRunUpgradesFn, releaseReconcilePlacementsFn, releasePrepareTierCatalogFn
	t.Cleanup(func() {
		releaseRunMigrateFn, releaseRunUpgradesFn, releaseReconcilePlacementsFn, releasePrepareTierCatalogFn = origMigrate, origUpgrades, origPlacements, origCatalog
	})
	installFakeSQLClient(t, "f", `{}`)

	var steps []string
	releasePrepareTierCatalogFn = func(*cobra.Command, *resolvedCluster, *ssh.Pool) (func(context.Context, bool) error, error) {
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

	want := "catalog:render migrate:expand upgrades placements migrate:postdeploy catalog:reconcile"
	if got := strings.Join(steps, " "); got != want {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	if !strings.Contains(out.String(), "then the Purser tier catalog reconcile (purser bootstrap + validate)") {
		t.Fatalf("release plan does not name the tier catalog reconcile:\n%s", out.String())
	}
}

func TestPrepareReleaseTierCatalogSkipsManifestWithoutPurser(t *testing.T) {
	step, err := prepareReleaseTierCatalog(&cobra.Command{}, &resolvedCluster{Manifest: postExpandReportManifest()}, nil)
	if err != nil || step != nil {
		t.Fatalf("prepare without purser = (%v, %v), want no step", step != nil, err)
	}
}
