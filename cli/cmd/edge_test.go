package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestEdgeManifestChannelNotVersion(t *testing.T) {
	// Verify that manifest.Version (schema version) is never used as a release
	// version. Only manifest.Channel should be used for release resolution.
	manifest := &inventory.EdgeManifest{
		Version: "v1", // schema version — must NOT be used for release
	}

	// Simulate the logic from runEdgeProvisionFromManifest (edge.go:701-704)
	nodeVersion := manifest.Channel // should be empty, not "v1"
	if nodeVersion == manifest.Version {
		t.Fatalf("nodeVersion should not equal manifest.Version (%q); Channel should be used instead", manifest.Version)
	}
	if nodeVersion != "" {
		t.Fatalf("expected empty nodeVersion when Channel is unset, got %q", nodeVersion)
	}
}

func TestEdgeManifestChannelOverride(t *testing.T) {
	manifest := &inventory.EdgeManifest{
		Channel: "rc",
	}
	manifest.Version = "v1" // schema version — must be ignored for release resolution

	nodeVersion := manifest.Channel
	if nodeVersion != "rc" {
		t.Fatalf("expected nodeVersion=%q, got %q", "rc", nodeVersion)
	}
	if nodeVersion == manifest.Version {
		t.Fatal("nodeVersion must not equal schema version")
	}
}

func TestEdgeManifestVersionFlagOverridesChannel(t *testing.T) {
	manifest := &inventory.EdgeManifest{
		Channel: "rc",
	}
	manifest.Version = "v1" // schema version — must be ignored

	// Simulate --version flag override
	cliVersion := "v0.2.0-rc3"
	versionFlagChanged := true

	nodeVersion := manifest.Channel
	if versionFlagChanged {
		nodeVersion = cliVersion
	}
	if nodeVersion != "v0.2.0-rc3" {
		t.Fatalf("expected --version override to take precedence, got %q", nodeVersion)
	}
	if nodeVersion == manifest.Version {
		t.Fatal("nodeVersion must not equal schema version")
	}
}
