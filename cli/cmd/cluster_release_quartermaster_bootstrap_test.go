package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// `release apply` reconciles the Quartermaster bootstrap desired state (the
// service registry and its health endpoint overrides): it renders before the
// first mutation, applies once the upgraded Quartermaster serves, and the
// service-cluster assignments reconcile after the placement cleanup. A dry run
// hands every step dry-run so they only report.
func TestReleaseApplyReconcilesQuartermasterBootstrap(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	origMigrate, origUpgrades, origPlacements := releaseRunMigrateFn, releaseRunUpgradesFn, releaseReconcilePlacementsFn
	origQuartermaster, origAssignments := releasePrepareQuartermasterBootstrapFn, releaseReconcileAssignmentsFn
	t.Cleanup(func() {
		releaseRunMigrateFn, releaseRunUpgradesFn, releaseReconcilePlacementsFn = origMigrate, origUpgrades, origPlacements
		releasePrepareQuartermasterBootstrapFn, releaseReconcileAssignmentsFn = origQuartermaster, origAssignments
	})
	installFakeSQLClient(t, "f", `{}`)

	var steps []string
	releasePrepareQuartermasterBootstrapFn = func(*cobra.Command, *resolvedCluster, *ssh.Pool) (func(context.Context, bool) error, error) {
		steps = append(steps, "quartermaster:render")
		return func(_ context.Context, dryRun bool) error {
			if !dryRun {
				t.Errorf("quartermaster reconcile dry-run = false, want true")
			}
			steps = append(steps, "quartermaster:reconcile")
			return nil
		}, nil
	}
	releaseReconcileAssignmentsFn = func(_ context.Context, _ *cobra.Command, _ *resolvedCluster, dryRun bool) error {
		if !dryRun {
			t.Errorf("assignments dry-run = false, want true")
		}
		steps = append(steps, "assignments")
		return nil
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

	out, err := runReleaseApplyForTest(t, releaseApplyOptions{version: "v0.3.11", dryRun: true, yes: true})
	if err != nil {
		t.Fatalf("release apply dry-run: %v\n%s", err, out)
	}
	want := "quartermaster:render migrate:expand upgrades quartermaster:reconcile placements assignments migrate:postdeploy"
	if got := strings.Join(steps, " "); got != want {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	if !strings.Contains(out, "then the Quartermaster bootstrap reconcile (service registry, health endpoints, catalog)") {
		t.Fatalf("release plan does not name the Quartermaster bootstrap reconcile:\n%s", out)
	}
}

// A render failure refuses the release before anything changes.
func TestReleaseApplyRefusesWhenQuartermasterBootstrapDoesNotRender(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	origQuartermaster := releasePrepareQuartermasterBootstrapFn
	t.Cleanup(func() { releasePrepareQuartermasterBootstrapFn = origQuartermaster })
	releasePrepareQuartermasterBootstrapFn = func(*cobra.Command, *resolvedCluster, *ssh.Pool) (func(context.Context, bool) error, error) {
		return nil, errors.New("render bootstrap desired state: missing cluster owner")
	}
	_, err := runReleaseApplyForTest(t, releaseApplyOptions{version: "v0.3.11", yes: true})
	if err == nil || !strings.Contains(err.Error(), "quartermaster bootstrap: render bootstrap desired state") {
		t.Fatalf("err = %v, want the render failure", err)
	}
	if h.mutationCalled {
		t.Fatal("the release mutated the cluster before refusing")
	}
}

func runReleaseApplyForTest(t *testing.T, opts releaseApplyOptions) (string, error) {
	t.Helper()
	cmd := newClusterReleaseApplyCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set(unsafeCLIFloorFlag, "true"); err != nil {
		t.Fatal(err)
	}
	err := runReleaseApply(cmd, &resolvedCluster{Manifest: postExpandReportManifest()}, opts)
	return out.String(), err
}
