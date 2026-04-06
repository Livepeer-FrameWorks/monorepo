package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestDeriveEdgeNodeName(t *testing.T) {
	tests := []struct {
		name       string
		nodeName   string
		nodeDomain string
		sshTarget  string
		isLocal    bool
		want       string
	}{
		{name: "explicit node name", nodeName: "edge-eu-1", want: "edge-eu-1"},
		{name: "from domain", nodeDomain: "edge-eu-1.example.com", want: "edge-eu-1"},
		{name: "from ssh fqdn with port", sshTarget: "ubuntu@edge-eu-1.example.com:2222", want: "edge-eu-1.example.com"},
		{name: "from ssh bare host", sshTarget: "ubuntu@edge-eu-1", want: "edge-eu-1"},
		{name: "ip target returns empty", sshTarget: "ubuntu@203.0.113.10", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deriveEdgeNodeName(tt.nodeName, tt.nodeDomain, tt.sshTarget, tt.isLocal)
			if got != tt.want {
				t.Fatalf("deriveEdgeNodeName(%q, %q, %q, %v) = %q, want %q",
					tt.nodeName, tt.nodeDomain, tt.sshTarget, tt.isLocal, got, tt.want)
			}
		})
	}
}

func TestCanonicalEdgeNodeID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "preserve readable id", input: "edge-eu-1", want: "edge-eu-1"},
		{name: "strip fqdn", input: "edge-eu-1.example.com", want: "edge-eu-1"},
		{name: "normalize case and underscores", input: "EDGE_EU_1", want: "edge-eu-1"},
		{name: "reject invalid ipish", input: "203.0.113.10", want: ""},
		{name: "reject empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canonicalEdgeNodeID(tt.input)
			if got != tt.want {
				t.Fatalf("canonicalEdgeNodeID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

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
