package provisioner

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// NewNodeTuningProvisioner applies the OS-tuning policy that suppresses
// Ubuntu's background upgrade/reboot mechanisms and writes the sysctl/limits
// baseline. Runs immediately after node_baseline on every provisioned host.
func NewNodeTuningProvisioner(pool *ssh.Pool) (Provisioner, error) {
	return NewRolePlaybookProvisioner("node-tuning", pool,
		"frameworks.infra.node_tuning", "playbooks/node_tuning.yml",
		nodeTuningRoleVars, nodeTuningRoleDetect)
}

func nodeTuningRoleVars(_ context.Context, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	vars := map[string]any{}
	if profile, ok := config.Metadata["profile"].(string); ok && profile != "" {
		vars["node_tuning_profile"] = profile
	}
	vars["node_tuning_reserved_ports"] = reservedPortsSysctl(config.Metadata["reserved_ports"])
	return vars, nil
}

// reservedPortsSysctl renders ports as the sorted, comma-separated value of
// net.ipv4.ip_local_reserved_ports; an empty string leaves the key unset.
func reservedPortsSysctl(raw any) string {
	ports, ok := raw.([]int)
	if !ok {
		return ""
	}
	sorted := slices.Clone(ports)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	parts := make([]string, 0, len(sorted))
	for _, port := range sorted {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, ",")
}

func nodeTuningRoleDetect(context.Context, inventory.Host, ServiceConfig, RoleBuildHelpers) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: false, Running: false}, nil
}
