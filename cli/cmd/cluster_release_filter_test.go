package cmd

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

// classifyUpgradeableServices EXCLUDES only explicit provision-only roles (pinned external images like nginx /
// victoriametrics) and KEEPS every FrameWorks artifact — a missing one fails closed later in runUpgrade, never a
// silent skip. Aliased services resolve by deploy name.
func TestClassifyUpgradeableServices_ExcludesOnlyProvisionOnlyRoles(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu":  {Enabled: true, Deploy: "foghorn"},
			"chandler-eu": {Enabled: true, Deploy: "chandler"},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"chartroom": {Enabled: true},
			"nginx":     {Enabled: true},
		},
		Observability: map[string]inventory.ServiceConfig{
			"victoriametrics": {Enabled: true},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&testDiscard{})

	got, err := classifyUpgradeableServices(cmd, manifest,
		[]string{"foghorn-eu", "chandler-eu", "chartroom", "nginx", "victoriametrics"})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}

	want := []string{"foghorn-eu", "chandler-eu", "chartroom"}
	if !slices.Equal(got, want) {
		t.Fatalf("kept = %v, want %v (nginx + victoriametrics are provision-only and must be excluded)", got, want)
	}
	// A FrameWorks service is NEVER silently skipped by the classifier — even if its release artifact were missing,
	// it is kept so runUpgrade fails closed on it.
	for _, keep := range []string{"foghorn-eu", "chandler-eu", "chartroom"} {
		if !slices.Contains(got, keep) {
			t.Fatalf("%s is a FrameWorks artifact and must be kept, not skipped", keep)
		}
	}
}

// A deploy-name that cannot be resolved (unknown service id, no manifest entry, not a servicedefs id) is a HARD error,
// not an ignored skip.
func TestClassifyUpgradeableServices_UnresolvableDeployNameFailsClosed(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			// A manifest service whose id is not a servicedefs id and carries no Deploy override → resolveDeployName errors.
			"not-a-real-service": {Enabled: true},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&testDiscard{})

	if _, err := classifyUpgradeableServices(cmd, manifest, []string{"not-a-real-service"}); err == nil {
		t.Fatal("an unresolvable deploy name must fail closed, not be silently skipped")
	}
}
