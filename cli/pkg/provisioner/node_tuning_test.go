package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestNodeTuningRoleVarsRenderReservedPorts(t *testing.T) {
	vars, err := nodeTuningRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Metadata: map[string]any{
		"profile":        "core",
		"reserved_ports": []int{51821, 18006},
	}}, RoleBuildHelpers{})
	if err != nil {
		t.Fatal(err)
	}
	if got := vars["node_tuning_reserved_ports"]; got != "18006,51821" {
		t.Fatalf("node_tuning_reserved_ports = %#v, want \"18006,51821\"", got)
	}

	vars, err = nodeTuningRoleVars(context.Background(), inventory.Host{}, ServiceConfig{Metadata: map[string]any{"profile": "edge"}}, RoleBuildHelpers{})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := vars["node_tuning_reserved_ports"]; !ok || got != "" {
		t.Fatalf("a host with nothing to reserve renders node_tuning_reserved_ports = %#v (present %v), want an explicit empty list", got, ok)
	}
}
