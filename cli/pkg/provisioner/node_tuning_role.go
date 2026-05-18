package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// runNodeTuningRole invokes the frameworks.infra.node_tuning playbook
// against a single host. Used by EdgeProvisioner step [2/7] for standalone
// edge deploys; cluster_provision's ensureNodeTuning uses the higher-level
// NodeTuningProvisioner for fleet-wide application.
func runNodeTuningRole(ctx context.Context, pool *ssh.Pool, host inventory.Host, profile string) error {
	if profile == "" {
		profile = "core"
	}

	root, err := FindAnsibleRoot()
	if err != nil {
		return fmt.Errorf("node_tuning: locate ansible root: %w", err)
	}
	executor, err := ansiblerun.NewExecutor()
	if err != nil {
		return fmt.Errorf("node_tuning: %w", err)
	}
	ensurer := &ansiblerun.CollectionEnsurer{
		RequirementsFile: filepath.Join(root, "requirements.yml"),
	}
	cache, err := ensurer.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("node_tuning: ensure ansible collections + roles: %w", err)
	}

	invDir, err := os.MkdirTemp("", "frameworks-node-tuning-*")
	if err != nil {
		return fmt.Errorf("node_tuning: mkdtemp: %w", err)
	}
	defer os.RemoveAll(invDir)

	hostName := host.Name
	if hostName == "" {
		hostName = "node"
	}
	address := hostAddressFor(host)
	connection := ""
	privateKey := pool.DefaultKeyPath()
	if address == "localhost" || address == "127.0.0.1" {
		connection = "local"
		privateKey = ""
	}
	renderer := &ansiblerun.InventoryRenderer{}
	invPath, err := renderer.Render(invDir, ansiblerun.Inventory{
		Hosts: []ansiblerun.Host{
			{
				Name:       hostName,
				Address:    address,
				User:       host.User,
				PrivateKey: privateKey,
				Connection: connection,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("node_tuning: render inventory: %w", err)
	}

	envVars := map[string]string{
		"ANSIBLE_COLLECTIONS_PATH": ansibleCollectionsPath(root, cache.CollectionsPath),
		"ANSIBLE_ROLES_PATH":       cache.RolesPath,
	}
	for _, k := range []string{"SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "HOME", "USER", "PATH"} {
		if v := os.Getenv(k); v != "" {
			envVars[k] = v
		}
	}

	return executor.Execute(ctx, ansiblerun.ExecuteOptions{
		Playbook:   filepath.Join(root, "playbooks/node_tuning.yml"),
		Inventory:  invPath,
		ExtraVars:  map[string]any{"node_tuning_profile": profile},
		PrivateKey: privateKey,
		User:       host.User,
		Become:     true,
		WorkDir:    root,
		EnvVars:    envVars,
	})
}
