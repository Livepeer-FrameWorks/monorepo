package cmd

import "testing"

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
