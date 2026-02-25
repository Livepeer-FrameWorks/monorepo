package orchestrator

import (
	"context"

	"frameworks/cli/pkg/inventory"
)

// Phase represents a provisioning phase
type Phase string

const (
	PhaseInfrastructure Phase = "infrastructure" // Postgres, Kafka, ZK, ClickHouse
	PhaseApplications   Phase = "applications"   // FrameWorks services
	PhaseInterfaces     Phase = "interfaces"     // Nginx, webapp, website
	PhaseAll            Phase = "all"            // All phases
)

// Task represents a provisioning task
type Task struct {
	Name       string
	Type       string   // "postgres", "kafka", "quartermaster", etc.
	Host       string   // Host name from manifest
	ClusterID  string   // Resolved cluster for this task (empty for infrastructure)
	DependsOn  []string // Task names this depends on
	Phase      Phase
	Idempotent bool // Can be run multiple times safely
}

// TaskResult represents the result of executing a task
type TaskResult struct {
	Task      *Task
	Success   bool
	Skipped   bool // Skipped because already provisioned
	Error     error
	Message   string
	StartedAt int64
	Duration  int64 // milliseconds
}

// ExecutionPlan holds tasks organized by execution batches
type ExecutionPlan struct {
	Manifest *inventory.Manifest
	Batches  [][]*Task // Tasks grouped by dependency level (execute in order)
	AllTasks []*Task   // All tasks in plan
}

// ProvisionOptions configures provisioning behavior
type ProvisionOptions struct {
	Phase        Phase    // Which phase to provision
	DryRun       bool     // Show plan without executing
	SkipInit     bool     // Skip initialization (databases, topics, tables)
	Force        bool     // Force re-provision even if already exists
	Parallel     bool     // Run tasks within a batch in parallel
	OnlyHosts    []string // Only provision these hosts
	OnlyServices []string // Only provision these services
}

// Orchestrator coordinates multi-phase provisioning
type Orchestrator interface {
	// Plan creates an execution plan from a manifest
	Plan(ctx context.Context, manifest *inventory.Manifest, opts ProvisionOptions) (*ExecutionPlan, error)

	// Execute runs an execution plan
	Execute(ctx context.Context, plan *ExecutionPlan) ([]*TaskResult, error)

	// Validate checks if all services are healthy after provisioning
	Validate(ctx context.Context, manifest *inventory.Manifest) error
}
