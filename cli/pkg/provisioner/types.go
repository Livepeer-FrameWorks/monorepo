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
	Mode       string                 // "docker" or "native"
	Version    string                 // Service version
	Image      string                 // Docker image (optional override)
	BinaryURL  string                 // Native binary URL (optional override)
	DeployName string                 // Container/service name (optional override)
	Port       int                    // Primary port
	Ports      []int                  // Additional ports
	EnvFile    string                 // Path to env file
	DependsOn  []string               // Service dependencies
	Metadata   map[string]interface{} // Service-specific config
	Force      bool                   // Force re-provision even if exists
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
