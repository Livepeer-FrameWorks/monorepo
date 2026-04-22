package provisioner

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/ansible"
	"frameworks/cli/pkg/inventory"
)

// runGossValidate installs the pinned goss binary (if not already present at
// the right checksum) and runs a post-provision validator against spec.
//
// Caller supplies the service name (used for the spec filename), the release
// channel + metadata (so the goss artifact is resolved from the pinned
// manifest), and a ready-rendered goss YAML spec string. Failures surface as
// an Ansible playbook error — the whole Validate step aborts if any
// assertion fails, which is the desired behavior for post-provision checks.
func runGossValidate(
	ctx context.Context,
	executor *ansible.Executor,
	sshKeyPath string,
	host inventory.Host,
	serviceName string,
	channel string,
	metadata map[string]any,
	remoteArch string,
	specYAML string,
) error {
	artifact, err := resolveInfraArtifactFromChannel("goss", "linux-"+remoteArch, channel, metadata)
	if err != nil {
		return fmt.Errorf("resolve goss artifact: %w", err)
	}

	tasks := ansible.GossInstallTasks(artifact.URL, artifact.Checksum)
	tasks = append(tasks, ansible.GossValidateTasks(serviceName, specYAML)...)

	playbook := &ansible.Playbook{
		Name:  "goss validate: " + serviceName,
		Hosts: host.ExternalIP,
		Plays: []ansible.Play{
			{
				Name:        "goss validate " + serviceName,
				Hosts:       host.ExternalIP,
				Become:      true,
				GatherFacts: false,
				Tasks:       tasks,
			},
		},
	}
	inv := ansible.NewInventory()
	inv.AddHost(&ansible.InventoryHost{
		Name:    host.ExternalIP,
		Address: host.ExternalIP,
		Vars: map[string]string{
			"ansible_user":                 host.User,
			"ansible_ssh_private_key_file": sshKeyPath,
		},
	})
	result, execErr := executor.ExecutePlaybook(ctx, playbook, inv, ansible.ExecuteOptions{Verbose: false})
	if execErr != nil {
		return fmt.Errorf("goss validate for %s failed: %w\nOutput: %s", serviceName, execErr, result.Output)
	}
	if !result.Success {
		return fmt.Errorf("goss validate for %s failed\nOutput: %s", serviceName, result.Output)
	}
	return nil
}
