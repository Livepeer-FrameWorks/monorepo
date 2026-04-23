package provisioner

import (
	"context"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// Provisioner handles detection, provisioning, validation, and initialization of a service
type Provisioner interface {
	// Detect checks if service exists and returns current state
	Detect(ctx context.Context, host inventory.Host) (*detect.ServiceState, error)

	// Provision installs/configures service (idempotent)
	Provision(ctx context.Context, host inventory.Host, config ServiceConfig) error

	// Validate checks if service is healthy and reachable
	Validate(ctx context.Context, host inventory.Host, config ServiceConfig) error

	// Initialize creates data/schemas/topics (idempotent)
	Initialize(ctx context.Context, host inventory.Host, config ServiceConfig) error

	// Cleanup stops a service (for rollback on failure). Does not remove data.
	// Returns nil if cleanup not supported or not needed.
	Cleanup(ctx context.Context, host inventory.Host, config ServiceConfig) error

	// GetName returns the provisioner name
	GetName() string
}

// ServiceConfig holds service-specific configuration
type ServiceConfig struct {
	Mode       string            // "docker" or "native"
	Version    string            // Service version
	Image      string            // Docker image (optional override)
	BinaryURL  string            // Native binary URL (optional override)
	DeployName string            // Container/service name (optional override)
	Port       int               // Primary port
	Ports      []int             // Additional ports
	EnvFile    string            // Path to env file
	EnvVars    map[string]string // Merged env vars to write to the service env file
	DependsOn  []string          // Service dependencies
	Metadata   map[string]any    // Service-specific config
	Force      bool              // Force re-provision even if exists
	DeferStart bool              // Deploy but don't start (missing required config)
}

// ProvisionContext holds context for provisioning operations
type ProvisionContext struct {
	Host    inventory.Host
	Config  ServiceConfig
	SSHPool *ssh.Pool
	DryRun  bool
	Verbose bool
	Force   bool // Force re-provision even if exists
}

// ProvisionResult holds the result of a provisioning operation
type ProvisionResult struct {
	Provisioner string
	Host        string
	Success     bool
	Skipped     bool // Skipped because already provisioned
	Changed     bool // Changes were made
	Error       error
	Message     string
	Logs        []string
}

// InitializeResult holds the result of an initialization operation
type InitializeResult struct {
	Provisioner  string
	Host         string
	Success      bool
	Skipped      bool
	Error        error
	Message      string
	ItemsCreated []string // Databases, topics, tables created
}

// Seeder is the optional capability a Provisioner implements when it can
// apply out-of-band SQL seeds to the service it manages. The cluster seed
// command type-asserts to this. Seed payloads (database + SQL) travel via
// ServiceConfig.Metadata — the role's VarsBuilder forwards them into role
// variables the tasks/seed.yml path consumes.
type Seeder interface {
	ApplySeeds(ctx context.Context, host inventory.Host, config ServiceConfig) error
}

// Migrator is the optional capability a Provisioner implements when it can
// apply versioned SQL migrations. The cluster migrate command type-asserts
// to this. When dryRun is true, the underlying Ansible invocation runs in
// --check --diff mode so pending migrations are reported without applying.
type Migrator interface {
	ApplyMigrations(ctx context.Context, host inventory.Host, config ServiceConfig, dryRun bool) error
}

// CheckDiffer is the optional capability a Provisioner implements when it
// can report the would-change set for a host via ansible-playbook
// --check --diff. cluster provision --dry-run and cluster upgrade
// --dry-run type-assert to this.
type CheckDiffer interface {
	CheckDiff(ctx context.Context, host inventory.Host, config ServiceConfig) error
}

// Restarter is the optional capability a Provisioner implements when it can
// cleanly restart its managed service(s) via Ansible. cluster restart
// type-asserts to this so services with non-standard unit names
// (clickhouse-server, postgresql, yb-master + yb-tserver) are handled by
// their role's tasks/restart.yml instead of a Go-side unit-name guess.
type Restarter interface {
	Restart(ctx context.Context, host inventory.Host, config ServiceConfig) error
}
