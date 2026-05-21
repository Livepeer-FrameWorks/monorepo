package orchestrator

import (
	"context"

	"frameworks/cli/pkg/inventory"
)

// Phase represents a provisioning phase
type Phase string

const (
	PhaseMesh           Phase = "mesh"           // Privateer startup substrate (WG up before any infra)
	PhaseInfrastructure Phase = "infrastructure" // Postgres, Kafka, ClickHouse
	PhaseApplications   Phase = "applications"   // FrameWorks services
	PhaseInterfaces     Phase = "interfaces"     // Caddy, chartroom, foredeck
	PhaseAll            Phase = "all"            // All phases
)

// Task represents a provisioning task.
//
// Identity model:
//   - Type:       deploy slug for provisioner dispatch (e.g. "kafka", "kafka-controller", "yugabyte", "bridge")
//   - ServiceID:  canonical manifest key (e.g. "kafka", "postgres", "bridge") — used for manifest lookups
//   - InstanceID: per-instance identity (e.g. "1", "100", "regional-eu-1", "" for singletons)
//   - Name:       derived display/graph key — do not parse for identity; use ServiceID and InstanceID instead
type Task struct {
	Name       string   // Derived: Type + "-" + InstanceID (or ServiceID + "@" + InstanceID for app tasks)
	Type       string   // Deploy slug for provisioner dispatch
	ServiceID  string   // Canonical manifest key for lookups and business logic
	InstanceID string   // Per-instance identity ("1", "foghorn", "regional-eu-1", "" for singletons)
	Host       string   // Host name from manifest
	ClusterID  string   // Resolved cluster for this task (empty for infrastructure)
	DependsOn  []string // Task names this depends on
	Phase      Phase
	Idempotent bool           // Can be run multiple times safely
	Metadata   map[string]any // Per-task data the provisioner consumes (e.g. redis_role, primary_host)
}

// NewTask creates a task with a derived Name. Use for infrastructure tasks where Name = Type-InstanceID.
func NewTask(taskType, serviceID, instanceID, host string, phase Phase) *Task {
	name := taskType
	if instanceID != "" {
		name = taskType + "-" + instanceID
	}
	return &Task{
		Name:       name,
		Type:       taskType,
		ServiceID:  serviceID,
		InstanceID: instanceID,
		Host:       host,
		Phase:      phase,
		Idempotent: true,
	}
}

// NewServiceTask creates a task for app/interface/observability services.
// For multi-host services, Name = ServiceID@InstanceID. For singletons, Name = ServiceID.
func NewServiceTask(taskType, serviceID, instanceID, host string, phase Phase) *Task {
	name := serviceID
	if instanceID != "" {
		name = serviceID + "@" + instanceID
	}
	return &Task{
		Name:       name,
		Type:       taskType,
		ServiceID:  serviceID,
		InstanceID: instanceID,
		Host:       host,
		Phase:      phase,
		Idempotent: true,
	}
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
	Batches  [][]*Task // Parallel-executable batches in dep order; within a batch, tasks share no host and no unresolved deps
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

// UpdateStrategy describes how a service's hosts are partitioned into
// rolling-update waves. The existing ExecutionPlan / Batches model is
// unchanged and continues to drive `cluster provision`; `cluster apply`
// composes RolloutPlans from the diff classifier's filtered task set.
//
// Zero-values mean "default to safest": MaxUnavailable=0 is treated as
// "one host at a time" inside BuildWaves so a missing strategy can't
// accidentally roll a whole tier at once.
type UpdateStrategy struct {
	// MaxUnavailable is the upper bound on hosts being rolled at once
	// within a single wave. 0 means "one host at a time" — the safe
	// default. A typical stateless tier sets this to ceil(N/3).
	MaxUnavailable int

	// Canary, if non-zero, makes the first wave contain at most Canary
	// hosts before the normal MaxUnavailable cadence resumes. Soaks one
	// host (or a small group) before rolling the rest.
	Canary int

	// RegionStagger when true forces hosts in different regions into
	// distinct waves: roll all of region A, then all of region B. Paired
	// regional tiers (Foghorn EU vs US) use this to keep one region fully
	// healthy at all times.
	RegionStagger bool

	// PrimaryLast when true holds tasks with Role=="primary" until after
	// every non-primary task has run. For Redis: replicas roll first,
	// sentinel-triggered failover happens, then the old primary rolls.
	PrimaryLast bool
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
