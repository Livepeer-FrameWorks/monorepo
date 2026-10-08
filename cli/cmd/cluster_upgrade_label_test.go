package cmd

import (
	"testing"

	"frameworks/cli/pkg/gitops"
)

// Production v0.3.11: a carried-forward service printed "upgraded from v0.3.10
// to v0.3.11-rc22", which reads as if a release candidate was deployed instead
// of v0.3.11. The label names the release and the unchanged build.
func TestUpgradeTargetLabelNamesReleaseForCarriedBuild(t *testing.T) {
	manifest := &gitops.Manifest{
		PlatformVersion: "v0.3.11",
		Services: []gitops.ServiceEntry{
			{Name: "commodore", ServiceVersion: "v0.3.11-rc22", Image: "img", Digest: "sha256:aa", CarriedFrom: "v0.3.11-rc22"},
			{Name: "foghorn", ServiceVersion: "v0.3.11", Image: "img", Digest: "sha256:bb"},
		},
	}
	carried, err := manifest.GetServiceInfo("commodore")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := upgradeTargetLabel(carried), "release v0.3.11 (build v0.3.11-rc22, unchanged since)"; got != want {
		t.Fatalf("carried label = %q, want %q", got, want)
	}
	rebuilt, err := manifest.GetServiceInfo("foghorn")
	if err != nil {
		t.Fatal(err)
	}
	if got := upgradeTargetLabel(rebuilt); got != "v0.3.11" {
		t.Fatalf("rebuilt label = %q, want v0.3.11", got)
	}
}
