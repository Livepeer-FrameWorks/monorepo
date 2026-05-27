package provisioner

import (
	"context"

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
	return vars, nil
}

func nodeTuningRoleDetect(context.Context, inventory.Host, ServiceConfig, RoleBuildHelpers) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: false, Running: false}, nil
}
