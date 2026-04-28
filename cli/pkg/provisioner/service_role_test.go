package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestServiceNativeVarsIncludesRuntimePackages(t *testing.T) {
	vars, err := serviceNativeVars(context.Background(), ServiceRoleConfig{
		ServiceName:           "livepeer-gateway",
		DefaultPort:           8935,
		RuntimePackages:       []string{"common-runtime"},
		DebianRuntimePackages: []string{"libva-drm2"},
		PacmanRuntimePackages: []string{"libva"},
	}, inventory.Host{Name: "central-eu-1"}, ServiceConfig{
		Mode:      "native",
		Version:   "vtest",
		BinaryURL: "https://example.test/livepeer.tar.gz",
		Metadata:  map[string]any{},
	}, RoleBuildHelpers{})
	if err != nil {
		t.Fatalf("serviceNativeVars: %v", err)
	}

	assertStringSlice(t, vars["go_service_runtime_packages"], []string{"common-runtime"})
	assertStringSlice(t, vars["go_service_debian_runtime_packages"], []string{"libva-drm2"})
	assertStringSlice(t, vars["go_service_pacman_runtime_packages"], []string{"libva"})
}

func assertStringSlice(t *testing.T, got any, want []string) {
	t.Helper()
	gotSlice, ok := got.([]string)
	if !ok {
		t.Fatalf("got %T, want []string", got)
	}
	if len(gotSlice) != len(want) {
		t.Fatalf("got %v, want %v", gotSlice, want)
	}
	for i := range want {
		if gotSlice[i] != want[i] {
			t.Fatalf("got %v, want %v", gotSlice, want)
		}
	}
}
