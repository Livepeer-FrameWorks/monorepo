package cmd

import (
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

func TestClusterNodesAddDefaultsToNativeStable(t *testing.T) {
	t.Parallel()

	cmd := newClusterNodesAddCmd()
	mode := cmd.Flags().Lookup("mode")
	if mode == nil {
		t.Fatal("mode flag missing")
	}
	if mode.DefValue != "native" {
		t.Fatalf("mode default = %q, want native", mode.DefValue)
	}
	version := cmd.Flags().Lookup("version")
	if version == nil {
		t.Fatal("version flag missing")
	}
	if version.DefValue != "stable" {
		t.Fatalf("version default = %q, want stable", version.DefValue)
	}
}

func TestRequireClusterLifecycleContextRejectsSelfHosted(t *testing.T) {
	t.Parallel()

	err := requireClusterLifecycleContext(fwcfg.Context{Persona: fwcfg.PersonaSelfHosted})
	if err == nil {
		t.Fatal("expected selfhosted persona to be rejected")
	}
	if !strings.Contains(err.Error(), "edge deploy") {
		t.Fatalf("expected Bridge edge deploy guidance, got %v", err)
	}
}
