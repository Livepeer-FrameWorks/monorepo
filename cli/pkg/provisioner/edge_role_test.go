package provisioner

import (
	"maps"
	"slices"
	"testing"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/internal/templates"

	fwversion "github.com/Livepeer-FrameWorks/monorepo/pkg/version"
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

// The role stamps this CLI's release into the provisioned-config marker
// Helmsman reports, so doctor can list edges rendered by an older CLI.
func TestEdgeRoleVarsCarryCLIVersionForConfigMarker(t *testing.T) {
	restore := stubEdgeManifest(t)
	defer restore()
	prev := fwversion.Version
	fwversion.Version = "v9.8.7"
	defer func() { fwversion.Version = prev }()
	vars, err := edgeRoleVars(&EdgeProvisionConfig{Mode: "container", Version: "vtest", NodeID: "edge-eu-1"}, "linux", "amd64")
	if err != nil {
		t.Fatalf("edgeRoleVars returned error: %v", err)
	}
	if vars["edge_cli_version"] != "v9.8.7" {
		t.Fatalf("edge_cli_version = %#v, want v9.8.7", vars["edge_cli_version"])
	}
}

func TestEdgeRoleVarsPrivateTelemetryResolution(t *testing.T) {
	restore := stubEdgeManifest(t)
	defer restore()
	vars, err := edgeRoleVars(&EdgeProvisionConfig{
		Mode:             "container",
		Version:          "vtest",
		TelemetryURL:     "https://telemetry.media-eu.example.com/api/v1/write",
		TelemetryAddress: "192.168.10.17",
	}, "linux", "amd64")
	if err != nil {
		t.Fatalf("edgeRoleVars returned error: %v", err)
	}
	if got := vars["edge_telemetry_address"]; got != "192.168.10.17" {
		t.Fatalf("edge_telemetry_address = %#v", got)
	}
	if got := vars["edge_telemetry_hostname"]; got != "telemetry.media-eu.example.com" {
		t.Fatalf("edge_telemetry_hostname = %#v", got)
	}
}

func TestEdgeRoleVarsPrivateTelemetryRequiresHTTPSURL(t *testing.T) {
	restore := stubEdgeManifest(t)
	defer restore()
	_, err := edgeRoleVars(&EdgeProvisionConfig{
		Mode:             "container",
		Version:          "vtest",
		TelemetryURL:     "http://telemetry.media-eu.example.com/api/v1/write",
		TelemetryAddress: "192.168.10.17",
	}, "linux", "amd64")
	if err == nil {
		t.Fatal("expected private telemetry resolution with an HTTP URL to fail")
	}
}

// The Ansible render paths receive every Foghorn instance of the cell. The
// edge role writes this one variable into edge.env for the container and
// hands it to the Helmsman role for native installs.
func TestEdgeRoleVarsRenderEveryFoghornInstance(t *testing.T) {
	restore := stubEdgeManifest(t)
	defer restore()
	const addrs = "192.168.10.17:18029,192.168.10.18:18029,192.168.10.19:18029"
	vars, err := edgeRoleVars(&EdgeProvisionConfig{
		Mode:            "container",
		Version:         "vtest",
		NodeID:          "edge-eu-1",
		FoghornGRPCAddr: addrs,
	}, "linux", "amd64")
	if err != nil {
		t.Fatalf("edgeRoleVars returned error: %v", err)
	}
	if got := vars["edge_foghorn_grpc_addr"]; got != addrs {
		t.Fatalf("edge_foghorn_grpc_addr = %#v, want %q", got, addrs)
	}
}
