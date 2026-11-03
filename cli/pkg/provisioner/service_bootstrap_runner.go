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

// ServiceBootstrapMode picks which subcommand the service_bootstrap role runs.
type ServiceBootstrapMode string

const (
	// ServiceBootstrapModeApply uploads the rendered file, runs `<service>
	// bootstrap --check`, then `<service> bootstrap --file`. YAML is required.
	ServiceBootstrapModeApply ServiceBootstrapMode = "apply"
	// ServiceBootstrapModeValidate runs `<service> bootstrap validate` only —
	// the post-apply cross-service invariant check. No file upload.
	ServiceBootstrapModeValidate ServiceBootstrapMode = "validate"
	// ServiceBootstrapModeDryRun runs `<service> bootstrap --file --dry-run`
	// and prints its plan when the installed binary is TargetVersion, and
	// otherwise reports that it is not. YAML and TargetVersion are required.
	ServiceBootstrapModeDryRun ServiceBootstrapMode = "dry_run"
)

// ServiceBootstrapOptions describes one invocation of the service_bootstrap
// Ansible role.
type ServiceBootstrapOptions struct {
	Service string
	Host    inventory.Host
	// Mode selects apply (default when empty), validate or dry_run.
	Mode ServiceBootstrapMode
	// YAML is the rendered desired-state document. Required for apply and
	// dry_run.
	YAML string
	// ExtraArgs appends to the apply and dry_run invocations (today:
	// --reset-credentials and --accounts-only for commodore). Ignored in
	// validate mode.
	ExtraArgs []string
	// TargetVersion is the artifact version the dry_run plan needs installed.
	TargetVersion string
	AnsibleRoot   string
}

// RunServiceBootstrap uploads the rendered desired-state YAML to the host and
// invokes `<service> bootstrap --check` then `<service> bootstrap` via the
// service_bootstrap role. Mirrors the inventory + extra-vars + executor shape
// of RolePlaybookProvisioner.runWithOptions so the same SOPS env / SSH keys
// flow through. The bootstrap subcommand reads DATABASE_URL / SERVICE_TOKEN /
// QUARTERMASTER_GRPC_ADDR from the systemd EnvironmentFile, which the role
// sources before exec'ing the binary.
func RunServiceBootstrap(ctx context.Context, pool *ssh.Pool, opts ServiceBootstrapOptions) error {
	if opts.Service == "" {
		return fmt.Errorf("RunServiceBootstrap: Service required")
	}
	mode := opts.Mode
	if mode == "" {
		mode = ServiceBootstrapModeApply
	}
	switch mode {
	case ServiceBootstrapModeApply:
		if opts.YAML == "" {
			return fmt.Errorf("RunServiceBootstrap: YAML required for apply mode")
		}
	case ServiceBootstrapModeValidate:
		// no YAML required.
	case ServiceBootstrapModeDryRun:
		if opts.YAML == "" || opts.TargetVersion == "" {
			return fmt.Errorf("RunServiceBootstrap: YAML and TargetVersion required for dry_run mode")
		}
	default:
		return fmt.Errorf("RunServiceBootstrap: unknown mode %q", mode)
	}
	if opts.Host.ExternalIP == "" && opts.Host.Name == "" {
		return fmt.Errorf("RunServiceBootstrap: Host must have ExternalIP or Name")
	}

	root := opts.AnsibleRoot
	if root == "" {
		r, err := FindAnsibleRoot()
		if err != nil {
			return fmt.Errorf("locate ansible root: %w", err)
		}
		root = r
	}

	exec, err := ansiblerun.NewExecutor()
	if err != nil {
		return fmt.Errorf("ansible executor: %w", err)
	}
	cache, err := (&ansiblerun.CollectionEnsurer{
		RequirementsFile: filepath.Join(root, "requirements.yml"),
	}).Ensure(ctx)
	if err != nil {
		return fmt.Errorf("ensure ansible collections + roles: %w", err)
	}

	groupName := fmt.Sprintf("%s-bootstrap", opts.Service)
	host := opts.Host
	host.Cluster = firstNonEmpty(host.Cluster, groupName)

	invDir, err := os.MkdirTemp("", fmt.Sprintf("frameworks-%s-bootstrap-*", opts.Service))
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(invDir) //nolint:errcheck // best-effort cleanup

	renderer := &ansiblerun.InventoryRenderer{}
	invPath, err := renderer.Render(invDir, ansiblerun.Inventory{
		Hosts: []ansiblerun.Host{
			{
				Name:       host.Name,
				Address:    hostAddressFor(host),
				User:       host.User,
				PrivateKey: pool.DefaultKeyPath(),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("render inventory: %w", err)
	}

	envVars := map[string]string{
		"ANSIBLE_COLLECTIONS_PATH": ansibleCollectionsPath(root, cache.CollectionsPath),
		"ANSIBLE_ROLES_PATH":       cache.RolesPath,
	}
	for _, k := range []string{"SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "HOME", "USER", "PATH"} {
		if v := os.Getenv(k); v != "" {
			envVars[k] = v
		}
	}

	vars := map[string]any{
		"service_bootstrap_service": opts.Service,
		"service_bootstrap_mode":    string(mode),
	}
	if mode == ServiceBootstrapModeApply || mode == ServiceBootstrapModeDryRun {
		vars["service_bootstrap_yaml"] = opts.YAML
		if len(opts.ExtraArgs) > 0 {
			vars["service_bootstrap_extra_args"] = opts.ExtraArgs
		}
	}
	if mode == ServiceBootstrapModeDryRun {
		vars["service_bootstrap_target_version"] = opts.TargetVersion
	}

	return exec.Execute(ctx, ansiblerun.ExecuteOptions{
		Playbook:   filepath.Join(root, "playbooks/service_bootstrap.yml"),
		Inventory:  invPath,
		ExtraVars:  vars,
		PrivateKey: pool.DefaultKeyPath(),
		User:       host.User,
		Become:     true,
		WorkDir:    root,
		EnvVars:    envVars,
	})
}
