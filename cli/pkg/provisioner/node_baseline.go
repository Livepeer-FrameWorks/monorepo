package provisioner

import (
	"context"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// NewNodeBaselineProvisioner installs the host-level tool contract used by
// provisioning checks and operator diagnostics.
func NewNodeBaselineProvisioner(pool *ssh.Pool) (Provisioner, error) {
	return NewRolePlaybookProvisioner("node-baseline", pool,
		"frameworks.infra.node_baseline", "playbooks/node_baseline.yml",
		nodeBaselineRoleVars, nodeBaselineRoleDetect)
}

func nodeBaselineRoleVars(_ context.Context, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	vars := map[string]any{}
	if extra, ok := config.Metadata["extra_packages"].([]string); ok && len(extra) > 0 {
		vars["node_baseline_extra_packages"] = extra
	}
	return vars, nil
}

func nodeBaselineRoleDetect(context.Context, inventory.Host, RoleBuildHelpers) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: false, Running: false}, nil
}
