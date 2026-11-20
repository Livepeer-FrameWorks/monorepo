package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReleaseOriginalRetentionActivationFollowsRetirement(t *testing.T) {
	for _, stopAt := range []string{"upgrades", "retirements", ""} {
		t.Run("stop_"+stopAt, func(t *testing.T) {
			h := &releaseApplyHarness{}
			h.install(t)
			originalUpgrade, originalPlacements := releaseRunUpgradesFn, releaseReconcilePlacementsFn
			t.Cleanup(func() { releaseRunUpgradesFn, releaseReconcilePlacementsFn = originalUpgrade, originalPlacements })
			installFakeSQLClient(t, "f", `{}`)
			var steps []string
			stopped := errors.New("worker retirement not complete")
			releaseRunMigrateFn = func(_ *cobra.Command, _ *resolvedCluster, _ bool, phase string, _ bool, _ string, _, _ bool) error {
				steps = append(steps, phase)
				return nil
			}
			releaseRunUpgradesFn = func(*cobra.Command, *resolvedCluster, *reconcileEnv, []ReleaseTransition, []string, string, releaseApplyOptions) (map[string]struct{}, error) {
				steps = append(steps, "upgrades")
				if stopAt == "upgrades" {
					return nil, stopped
				}
				return map[string]struct{}{}, nil
			}
			releaseReconcilePlacementsFn = func(context.Context, *cobra.Command, *resolvedCluster, map[string]struct{}, bool) error {
				steps = append(steps, "retirements")
				if stopAt == "retirements" {
					return stopped
				}
				return nil
			}
			cmd := newClusterReleaseApplyCmd()
			var output strings.Builder
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetContext(context.Background())
			if err := cmd.Flags().Set(unsafeCLIFloorFlag, "true"); err != nil {
				t.Fatal(err)
			}
			err := runReleaseApply(cmd, &resolvedCluster{Manifest: postExpandReportManifest()}, releaseApplyOptions{version: "v0.3.16", dryRun: true, yes: true})
			if stopAt != "" {
				if !errors.Is(err, stopped) {
					t.Fatalf("release failure: %v\n%s", err, output.String())
				}
				for _, step := range steps {
					if step == "postdeploy" {
						t.Fatalf("activated originals before old workers retired: %v", steps)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("release apply: %v\n%s", err, output.String())
			}
			if got := strings.Join(steps, " "); got != "expand upgrades retirements postdeploy" {
				t.Fatalf("activation ordering: %s", got)
			}
		})
	}
}
