package provisioner

import (
	"maps"
	"slices"
	"testing"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/internal/templates"
)

func TestEdgeCapabilityEnv(t *testing.T) {
	t.Run("empty_returns_nil_map", func(t *testing.T) {
		got, err := edgeCapabilityEnv(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})

	t.Run("unsupported_capability_rejected", func(t *testing.T) {
		if _, err := edgeCapabilityEnv([]string{"ingest", "bogus"}); err == nil {
			t.Fatal("expected error for unsupported capability")
		}
	})

	t.Run("enables_selected_and_lowercases", func(t *testing.T) {
		// Mixed case + whitespace must normalize; unlisted caps stay false.
		got, err := edgeCapabilityEnv([]string{"ingest", " Storage "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{
			"edge_cap_ingest":     true,
			"edge_cap_storage":    true,
			"edge_cap_edge":       false,
			"edge_cap_processing": false,
		}
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s = %v, want %v", k, got[k], w)
			}
		}
	})
}

func TestEdgeRoleVarsPassHelmsmanEnvContract(t *testing.T) {
	restore := stubEdgeManifest(t)
	defer restore()
	vars, err := edgeRoleVars(&EdgeProvisionConfig{
		Mode:    "container",
		Version: "vtest",
		NodeID:  "edge-eu-1",
	}, "linux", "amd64")
	if err != nil {
		t.Fatalf("edgeRoleVars returned error: %v", err)
	}
	schema, err := configschema.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	required, ok := vars["edge_helmsman_required_env"].([]string)
	if !ok || len(required) == 0 || !slices.Equal(required, schema.RequiredEnv("helmsman")) {
		t.Fatalf("edge_helmsman_required_env = %#v, want schema %v", vars["edge_helmsman_required_env"], schema.RequiredEnv("helmsman"))
	}
	imageEnv, ok := vars["edge_image_env"].(map[string]string)
	if !ok || !maps.Equal(imageEnv, templates.EdgeImageEnv()) {
		t.Fatalf("edge_image_env = %#v, want %v", vars["edge_image_env"], templates.EdgeImageEnv())
	}
}
