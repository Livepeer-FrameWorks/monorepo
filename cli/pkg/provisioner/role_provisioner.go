package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// RoleVarsBuilder maps a service's manifest entry into the variable bag a
// frameworks.infra.<role> role expects. Implementations are per-service and
// are the one place orchestration knowledge crosses into Ansible variable
// form.
type RoleVarsBuilder func(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error)

// RoleDetector is the Go-layer reconnaissance hook. Runs before any playbook
// and does not mutate state — reports ServiceState for the orchestrator's
// "does this already exist?" decision.
type RoleDetector func(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error)

// RoleBuildHelpers is a tiny handle the builder/detector uses to reach the
// surrounding machinery (SSH pool for detection reads, distro/arch probes,
// artifact resolution). Passing it as a parameter keeps the builder pure
// (easier to test) while still letting it resolve runtime facts.
type RoleBuildHelpers struct {
	SSHPool         *ssh.Pool
	DetectRemoteOS  func(ctx context.Context, host inventory.Host) (string, string, error)
	DetectDistro    func(ctx context.Context, host inventory.Host) (string, error)
	ResolveArtifact func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error)
}

// ResolvedArtifact is the subset of gitops artifact metadata roles consume:
// URL + checksum together pin both identity and integrity, and the version
// string surfaces in validation output + Goss files.
type ResolvedArtifact struct {
	URL      string
	Checksum string
	Version  string
	Arch     string
}

// RolePlaybookProvisioner is the generic Provisioner backed by an Ansible
// role. One instance per service; the role name and vars builder carry all
// the per-service details.
//
// Detect stays Go for the fast path (no playbook, no subprocess). Provision,
// Validate, and Initialize are the same role invoked with different tags:
//
//	Provision  → tags: install,configure,service,validate
//	Validate   → tags: validate
//	Initialize → tags: init
//	Cleanup    → tags: service (with a state=stopped var) — not implemented yet
type RolePlaybookProvisioner struct {
	*BaseProvisioner

	// RoleName is only used for logging; the playbook file drives role
	// selection via its own `roles:` block. Kept here so error messages
	// carry service identity.
	RoleName string

	// PlaybookRel is the playbook path relative to the ansible/ root, e.g.
	// "playbooks/yugabyte.yml". Ignored when PlaybookSelector is set.
	PlaybookRel string

	// PlaybookSelector picks the playbook per apply. Used by mode-dispatch
	// provisioners that route between compose_stack (docker) and go_service
	// (native) based on ServiceConfig.Mode.
	PlaybookSelector func(config ServiceConfig) string

	// Builder produces the extra-vars map for Provision/Validate/Initialize.
	// Same builder for all three — the role's own tag dispatch decides
	// which tasks run.
	Builder RoleVarsBuilder

	// Detector is optional. When nil, Detect returns {Exists:false,Running:false}
	// which causes the orchestrator to always run the role (safe default —
	// idempotent tasks short-circuit on state match).
	Detector RoleDetector

	// AnsibleRoot is the absolute path to the ansible/ tree. Resolved once
	// in NewRolePlaybookProvisioner via FindAnsibleRoot.
	AnsibleRoot string

	// Executor is the ansiblerun executor — shared across services.
	Executor *ansiblerun.Executor

	// Ensurer resolves requirements.yml (collections + galaxy roles) into a
	// per-user cache on first use. Cached by sha256 so steady-state applies
	// skip ansible-galaxy entirely.
	Ensurer *ansiblerun.CollectionEnsurer
}

// NewRolePlaybookProvisioner constructs a provisioner that runs the named
// role via ansible-playbook. Returns an error if the ansible root cannot be
// located or if ansible-playbook is missing on PATH.
func NewRolePlaybookProvisioner(name string, pool *ssh.Pool, roleName, playbookRel string, builder RoleVarsBuilder, detector RoleDetector) (Provisioner, error) {
	root, err := FindAnsibleRoot()
	if err != nil {
		return nil, fmt.Errorf("%s: locate ansible root: %w", name, err)
	}
	exec, err := ansiblerun.NewExecutor()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &RolePlaybookProvisioner{
		BaseProvisioner: NewBaseProvisioner(name, pool),
		RoleName:        roleName,
		PlaybookRel:     playbookRel,
		Builder:         builder,
		Detector:        detector,
		AnsibleRoot:     root,
		Executor:        exec,
		Ensurer: &ansiblerun.CollectionEnsurer{
			RequirementsFile: filepath.Join(root, "requirements.yml"),
		},
	}, nil
}

// FindAnsibleRoot walks up from the current working directory looking for an
// `ansible/` sibling that contains our collection. Called once at provisioner
// construction. Precedence:
//  1. $FRAMEWORKS_ANSIBLE_ROOT (absolute path) — CI and tests override here.
//  2. $PWD/ansible then ancestors.
func FindAnsibleRoot() (string, error) {
	if override := os.Getenv("FRAMEWORKS_ANSIBLE_ROOT"); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve FRAMEWORKS_ANSIBLE_ROOT: %w", err)
		}
		if _, err := os.Stat(filepath.Join(abs, "ansible.cfg")); err != nil {
			return "", fmt.Errorf("FRAMEWORKS_ANSIBLE_ROOT=%s: %w", abs, err)
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		candidate := filepath.Join(dir, "ansible")
		if _, err := os.Stat(filepath.Join(candidate, "ansible.cfg")); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no ansible/ directory with ansible.cfg found above %s", cwd)
		}
		dir = parent
	}
}

// Detect delegates to the per-service detector or reports unknown.
func (r *RolePlaybookProvisioner) Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error) {
	if r.Detector == nil {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	return r.Detector(ctx, host, r.helpers())
}

// Provision runs the role with install+configure+service+validate tags.
func (r *RolePlaybookProvisioner) Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithTags(ctx, host, config, []string{"install", "configure", "service", "validate"})
}

// Validate re-runs only the validate tag. No state mutation.
func (r *RolePlaybookProvisioner) Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithTags(ctx, host, config, []string{"validate"})
}

// Initialize runs the init tag (one-time data setup — databases, topics,
// schemas). The role tags this "init" + "never" so it only runs when
// explicitly selected; regular Provision skips it.
func (r *RolePlaybookProvisioner) Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithTags(ctx, host, config, []string{"init"})
}

// Cleanup runs the role with the cleanup tag. Each role's tasks/cleanup.yml
// uses the correct unit/compose names for that service; this replaces the
// old generic `frameworks-<service>` systemd guess in BaseProvisioner.
func (r *RolePlaybookProvisioner) Cleanup(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithTags(ctx, host, config, []string{"cleanup"})
}

// ApplySeeds runs the role with the seed tag. Seed items (database + SQL
// content) are supplied via config.Metadata and forwarded into role vars by
// the service-specific VarsBuilder. Only services whose role ships a
// tasks/seed.yml respond meaningfully; others are a no-op.
func (r *RolePlaybookProvisioner) ApplySeeds(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithTags(ctx, host, config, []string{"seed"})
}

// ApplyMigrations runs the role with the migrate tag. When dryRun is true
// the ansible-playbook subprocess is invoked with --check --diff so the
// role's community.postgresql modules report the queries they would run
// instead of executing them.
func (r *RolePlaybookProvisioner) ApplyMigrations(ctx context.Context, host inventory.Host, config ServiceConfig, dryRun bool) error {
	return r.runWithOptions(ctx, host, config, roleRunOptions{
		Tags:  []string{"migrate"},
		Check: dryRun,
		Diff:  true,
	})
}

// CheckDiff runs the role's provision tags (install/configure/service) with
// --check --diff so the operator sees exactly which files, packages, and
// unit changes Ansible would apply without touching the host. No state
// mutation. Powers `cluster provision --dry-run` and `cluster upgrade
// --dry-run`.
func (r *RolePlaybookProvisioner) CheckDiff(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithOptions(ctx, host, config, roleRunOptions{
		Tags:  []string{"install", "configure", "service"},
		Check: true,
		Diff:  true,
	})
}

// Restart runs the role's restart tag, which knows the correct unit or
// compose names for the managed service(s). Used by `cluster restart` so
// operators don't have to guess frameworks-<service> vs postgresql vs
// yb-master+yb-tserver.
func (r *RolePlaybookProvisioner) Restart(ctx context.Context, host inventory.Host, config ServiceConfig) error {
	return r.runWithTags(ctx, host, config, []string{"restart"})
}

type roleRunOptions struct {
	Tags  []string
	Check bool
	Diff  bool
}

func (r *RolePlaybookProvisioner) runWithTags(ctx context.Context, host inventory.Host, config ServiceConfig, tags []string) error {
	return r.runWithOptions(ctx, host, config, roleRunOptions{Tags: tags})
}

func (r *RolePlaybookProvisioner) runWithOptions(ctx context.Context, host inventory.Host, config ServiceConfig, opts roleRunOptions) error {
	vars, err := r.Builder(ctx, host, config, r.helpers())
	if err != nil {
		return fmt.Errorf("%s: build role vars: %w", r.RoleName, err)
	}

	playbookRel := r.PlaybookRel
	if r.PlaybookSelector != nil {
		playbookRel = r.PlaybookSelector(config)
	}
	if playbookRel == "" {
		return fmt.Errorf("%s: no playbook selected for mode %q", r.RoleName, config.Mode)
	}

	// Preflight: resolve requirements.yml into the user-level cache. No-op
	// when the sha256 matches the last install's sentinel.
	cache, err := r.Ensurer.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("%s: ensure ansible collections + roles: %w", r.RoleName, err)
	}

	groupName := r.GetName()

	host.Cluster = firstNonEmpty(host.Cluster, groupName)

	invDir, err := os.MkdirTemp("", fmt.Sprintf("frameworks-%s-*", groupName))
	if err != nil {
		return fmt.Errorf("%s: mkdtemp: %w", r.RoleName, err)
	}
	defer os.RemoveAll(invDir)

	renderer := &ansiblerun.InventoryRenderer{}
	invPath, err := renderer.Render(invDir, ansiblerun.Inventory{
		Hosts: []ansiblerun.Host{
			{
				Name:       host.Name,
				Address:    hostAddressFor(host),
				User:       host.User,
				PrivateKey: r.sshPool.DefaultKeyPath(),
			},
		},
	})
	if err != nil {
		return fmt.Errorf("%s: render inventory: %w", r.RoleName, err)
	}

	envVars := map[string]string{
		// Point Ansible at the user-level cache first, then fall back to
		// the ansible/ tree so local role edits still take effect without
		// needing `ansible-galaxy install --force`.
		"ANSIBLE_COLLECTIONS_PATH": cache.CollectionsPath + string(os.PathListSeparator) + filepath.Join(r.AnsibleRoot, "collections"),
		"ANSIBLE_ROLES_PATH":       cache.RolesPath,
	}
	for _, k := range []string{"SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "HOME", "USER", "PATH"} {
		if v := os.Getenv(k); v != "" {
			envVars[k] = v
		}
	}

	return r.Executor.Execute(ctx, ansiblerun.ExecuteOptions{
		Playbook:   filepath.Join(r.AnsibleRoot, playbookRel),
		Inventory:  invPath,
		ExtraVars:  vars,
		Tags:       opts.Tags,
		Check:      opts.Check,
		Diff:       opts.Diff,
		Verbose:    0,
		PrivateKey: r.sshPool.DefaultKeyPath(),
		User:       host.User,
		Become:     true,
		WorkDir:    r.AnsibleRoot,
		EnvVars:    envVars,
	})
}

func (r *RolePlaybookProvisioner) helpers() RoleBuildHelpers {
	return RoleBuildHelpers{
		SSHPool:        r.sshPool,
		DetectRemoteOS: r.BaseProvisioner.DetectRemoteArch,
		DetectDistro:   r.BaseProvisioner.DetectDistroFamily,
		ResolveArtifact: func(name, arch, channel string, metadata map[string]any) (ResolvedArtifact, error) {
			artifact, err := resolveInfraArtifactFromChannel(name, arch, channel, metadata)
			if err != nil {
				return ResolvedArtifact{}, err
			}
			return ResolvedArtifact{
				URL:      artifact.URL,
				Checksum: artifact.Checksum,
				Arch:     arch,
			}, nil
		},
	}
}

func hostAddressFor(h inventory.Host) string {
	if h.ExternalIP != "" {
		return h.ExternalIP
	}
	return h.Name
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
