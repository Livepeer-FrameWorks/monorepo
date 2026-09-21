package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

func nativeGate(t *testing.T, service, version string) (path, protocol string, timeout any) {
	t.Helper()
	def := servicedefs.Services[service]
	vars, err := serviceNativeVars(context.Background(), genericServiceRoleConfig(service, def.DefaultPort), inventory.Host{Name: "ctrl-1"}, ServiceConfig{
		Mode:      "native",
		Version:   version,
		BinaryURL: "http://example.invalid/" + service + ".tar.gz",
		Metadata:  map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars(%s, %s): %v", service, version, err)
	}
	return vars["go_service_health_path"].(string), vars["go_service_health_protocol"].(string), vars["go_service_validate_timeout"]
}

func composeGate(t *testing.T, service, version string) (path string, timeout any) {
	t.Helper()
	def := servicedefs.Services[service]
	cfg := genericServiceRoleConfig(service, def.DefaultPort)
	cfg.DefaultImage = "ghcr.io/livepeer-frameworks/" + service + ":" + version
	vars, err := serviceComposeVars(context.Background(), cfg, inventory.Host{Name: "ctrl-1"}, ServiceConfig{
		Mode:     "docker",
		Version:  version,
		Metadata: map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceComposeVars(%s, %s): %v", service, version, err)
	}
	return vars["compose_stack_service"].(map[string]any)["health_path"].(string), vars["compose_stack_validate_timeout"]
}

// TestRolloutGateProbesReadinessOfTheDeployedBinary covers the native and Compose rollout gates for the three cases
// the /ready switch has to survive, using the ServiceRoleConfig the registry builds:
//   - an upgrade from v0.3.10 (no /ready) to v0.3.11 gates the new binary on /ready;
//   - the automatic rollback re-renders the gate for the restored v0.3.10 binary, which only serves /health;
//   - during a rolling upgrade each host is gated for the binary deployed to it.
func TestRolloutGateProbesReadinessOfTheDeployedBinary(t *testing.T) {
	since := servicedefs.Services["bridge"].ReadySince
	cases := []struct {
		name    string
		service string
		version string
		want    string
	}{
		{"upgrade target serves /ready", "bridge", since, "/ready"},
		{"release candidate of the introducing release", "commodore", since + "-rc1", "/ready"},
		{"automatic rollback to a binary without /ready", "bridge", "v0.3.10", "/health"},
		{"rollback of a database-backed service", "quartermaster", "v0.3.10", "/health"},
		{"unresolved version stays on liveness", "purser", "", "/health"},
		{"Chandler serves /ready in every supported release", "chandler", "v0.3.10", "/ready"},
		{"Grafana keeps its own health path", "grafana", since, "/api/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if path, protocol, _ := nativeGate(t, tc.service, tc.version); path != tc.want || protocol != "http" {
				t.Errorf("native gate = %s %s, want http %s", protocol, path, tc.want)
			}
			if path, _ := composeGate(t, tc.service, tc.version); path != tc.want {
				t.Errorf("compose gate = %s, want %s", path, tc.want)
			}
		})
	}

	fleet := map[string]string{"ctrl-1": since, "ctrl-2": "v0.3.10"}
	want := map[string]string{"ctrl-1": "/ready", "ctrl-2": "/health"}
	for host, version := range fleet {
		if path, _, _ := nativeGate(t, "bridge", version); path != want[host] {
			t.Errorf("rolling upgrade host %s on %s: native gate = %s, want %s", host, version, path, want[host])
		}
	}
}

// TestRolloutGateProbesInstalledBinaryWhenRestarting covers cluster restart --validate: the config resolves the
// channel's newest release, but the gate must follow the release detected on the host, since restart deploys nothing.
func TestRolloutGateProbesInstalledBinaryWhenRestarting(t *testing.T) {
	since := servicedefs.Services["bridge"].ReadySince
	def := servicedefs.Services["bridge"]
	cfg := genericServiceRoleConfig("bridge", def.DefaultPort)
	cfg.DefaultImage = "ghcr.io/livepeer-frameworks/bridge:" + since
	for installed, want := range map[string]string{"v0.3.10": "/health", since: "/ready", "": "/health", "dev": "/health"} {
		config := ServiceConfig{
			Version:          since,
			BinaryURL:        "http://example.invalid/bridge.tar.gz",
			Metadata:         map[string]any{"version": since},
			ProbeInstalled:   true,
			InstalledVersion: installed,
		}
		config.Mode = "native"
		native, err := serviceNativeVars(context.Background(), cfg, inventory.Host{Name: "ctrl-1"}, config, RoleBuildHelpers{})
		if err != nil {
			t.Fatalf("serviceNativeVars: %v", err)
		}
		if got := native["go_service_health_path"]; got != want {
			t.Errorf("native restart gate for installed %q = %v, want %s", installed, got, want)
		}
		config.Mode = "docker"
		compose, err := serviceComposeVars(context.Background(), cfg, inventory.Host{Name: "ctrl-1"}, config, RoleBuildHelpers{})
		if err != nil {
			t.Fatalf("serviceComposeVars: %v", err)
		}
		if got := compose["compose_stack_service"].(map[string]any)["health_path"]; got != want {
			t.Errorf("compose restart gate for installed %q = %v, want %s", installed, got, want)
		}
	}
}

// TestRolloutGateWaitsForReadiness pins the 150-second rollout wait for both gates. /ready stays 503 until a background
// gRPC listener has its TLS files (a wait of up to two minutes) and the service's dependency checks pass.
func TestRolloutGateWaitsForReadiness(t *testing.T) {
	if _, _, timeout := nativeGate(t, "bridge", "v0.3.11"); timeout != 150 {
		t.Fatalf("native validate timeout = %v, want 150", timeout)
	}
	if _, timeout := composeGate(t, "bridge", "v0.3.11"); timeout != 150 {
		t.Fatalf("compose validate timeout = %v, want 150", timeout)
	}
	if RolloutReadinessTimeout.Seconds() != 150 {
		t.Fatalf("RolloutReadinessTimeout = %s, want 150s", RolloutReadinessTimeout)
	}
}
