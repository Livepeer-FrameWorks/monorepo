package cmd

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// The Livepeer gateway and signer install go-livepeer from the platform
// release manifest, so they are not independently pinned managed dependencies
// that the release leaves alone.
func TestClassifyReleaseServicesSeparatesReleasePinnedDependencies(t *testing.T) {
	manifest := &inventory.Manifest{
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway-eu": {Enabled: true, Deploy: "livepeer-gateway"},
			"livepeer-signer":     {Enabled: true},
			"foghorn":             {Enabled: true},
		},
		Interfaces: map[string]inventory.ServiceConfig{"nginx": {Enabled: true}},
	}
	classes, err := classifyReleaseServices(manifest, []string{"foghorn", "livepeer-gateway-eu", "livepeer-signer", "nginx"})
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if slices.Contains(classes.Dependencies, "livepeer-gateway-eu") || slices.Contains(classes.Dependencies, "livepeer-signer") {
		t.Fatalf("independent managed dependencies = %v; the release pins go-livepeer", classes.Dependencies)
	}
	if !slices.Equal(classes.Dependencies, []string{"nginx"}) || !slices.Equal(classes.Platform, []string{"foghorn"}) {
		t.Fatalf("classes = %+v", classes)
	}
}
