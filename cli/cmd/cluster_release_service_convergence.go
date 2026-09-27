package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/orchestrator"
)

// Release service convergence applies the roles of the stateful data services
// and the managed dependencies to every host that runs them, so a role fix
// (log rotation, retention, flags, reload handling) reaches running clusters
// without `cluster provision --force`. Each host runs its role's check-mode
// precheck first and is skipped when converged. A host whose role reports a
// change converges alone: the service must pass its health gate before the
// role touches the host and again before the next host starts, and the first
// gate that does not pass stops the stage with every later host untouched. A
// rerun resumes where the cluster still diverges, because converged hosts are
// skipped and each remaining change is gated on a healthy service first.
const releaseHostStepService = "service"

// releaseServiceStage is one service of the stage, run in the order of
// releaseServiceStages.
type releaseServiceStage struct {
	name      string
	taskTypes []string
	// describe prints the stage's line of the release plan.
	describe func(out io.Writer, tasks []*orchestrator.Task)
	run      func(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error
}

func releaseServiceStages() []releaseServiceStage {
	return []releaseServiceStage{
		{name: "yugabyte", taskTypes: []string{"yugabyte"}, describe: describeYugabyteConvergence, run: runYugabyteConvergence},
		{name: "clickhouse", taskTypes: []string{"clickhouse"}, describe: describeClickHouseConvergence, run: runClickHouseConvergence},
		{name: "kafka", taskTypes: []string{"kafka-controller", "kafka"}, describe: describeKafkaConvergence, run: runKafkaConvergence},
		{name: "observability", taskTypes: []string{"vmagent", "vmalert", "vmauth"}, describe: describeObservabilityConvergence, run: runObservabilityConvergence},
		{name: "proxies", taskTypes: []string{"nginx", "caddy"}, describe: describeProxyConvergence, run: runProxyConvergence},
		{name: "compose dependencies", taskTypes: []string{"chatwoot", "listmonk", "metabase", "grafana"}, describe: describeComposeDependencyConvergence, run: runComposeDependencyConvergence},
	}
}

// planReleaseServiceSteps returns one step per planner task of every service
// stage, grouped by stage in stage order and, within a stage, by cluster, by
// the stage's task type order, then by host. It performs no I/O.
func planReleaseServiceSteps(plan *orchestrator.ExecutionPlan) []releaseHostConvergenceStep {
	if plan == nil {
		return nil
	}
	var steps []releaseHostConvergenceStep
	for _, stage := range releaseServiceStages() {
		rank := map[string]int{}
		for i, taskType := range stage.taskTypes {
			rank[taskType] = i
		}
		var tasks []*orchestrator.Task
		for _, task := range plan.AllTasks {
			if task == nil {
				continue
			}
			if _, ok := rank[task.Type]; ok {
				tasks = append(tasks, task)
			}
		}
		sort.SliceStable(tasks, func(i, j int) bool {
			if tasks[i].ClusterID != tasks[j].ClusterID {
				return tasks[i].ClusterID < tasks[j].ClusterID
			}
			if rank[tasks[i].Type] != rank[tasks[j].Type] {
				return rank[tasks[i].Type] < rank[tasks[j].Type]
			}
			return tasks[i].Host < tasks[j].Host
		})
		for _, task := range tasks {
			steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepService, Label: task.Host, Group: stage.name, Task: task})
		}
	}
	return steps
}

// releaseServiceTasks groups the service steps by stage name.
func releaseServiceTasks(steps []releaseHostConvergenceStep) map[string][]*orchestrator.Task {
	byStage := map[string][]*orchestrator.Task{}
	for _, step := range steps {
		if step.Kind == releaseHostStepService && step.Task != nil {
			byStage[step.Group] = append(byStage[step.Group], step.Task)
		}
	}
	return byStage
}

// writeReleaseServiceConvergencePlan prints one line per service stage.
func writeReleaseServiceConvergencePlan(out io.Writer, steps []releaseHostConvergenceStep) {
	byStage := releaseServiceTasks(steps)
	for _, stage := range releaseServiceStages() {
		if tasks := byStage[stage.name]; len(tasks) > 0 {
			stage.describe(out, tasks)
		}
	}
}

// runReleaseServices runs every service stage in order; a failure stops the
// stage with the remaining hosts untouched.
func (c *releaseHostConvergence) runReleaseServices(ctx context.Context, steps []releaseHostConvergenceStep, dryRun bool) error {
	byStage := releaseServiceTasks(steps)
	for _, stage := range releaseServiceStages() {
		tasks := byStage[stage.name]
		if len(tasks) == 0 {
			continue
		}
		if err := stage.run(ctx, c, tasks, dryRun); err != nil {
			return fmt.Errorf("%s: %w", stage.name, err)
		}
	}
	return nil
}

// releaseRuntimeOverridesKey names the task metadata whose entries
// convergeTaskBefore adds to the runtime data it renders the task with.
const releaseRuntimeOverridesKey = "release_runtime_overrides"

func releaseRuntimeOverrides(task *orchestrator.Task) map[string]any {
	if overrides, ok := task.Metadata[releaseRuntimeOverridesKey].(map[string]any); ok {
		return overrides
	}
	return nil
}

// withReleaseRuntimeOverrides returns a copy of task that renders with
// overrides added to its runtime data.
func withReleaseRuntimeOverrides(task *orchestrator.Task, overrides map[string]any) *orchestrator.Task {
	clone := *task
	clone.Metadata = make(map[string]any, len(task.Metadata)+1)
	for k, v := range task.Metadata {
		clone.Metadata[k] = v
	}
	merged := map[string]any{}
	for k, v := range releaseRuntimeOverrides(task) {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	clone.Metadata[releaseRuntimeOverridesKey] = merged
	return &clone
}

// serviceGateTiming bounds how long a gate waits for a service to be healthy.
type serviceGateTiming struct {
	Timeout  time.Duration
	Interval time.Duration
}

// pollServiceGate re-evaluates check until it reports no problems, timing
// passes, or ctx ends, and reports the last problems on timeout.
func pollServiceGate(ctx context.Context, timing serviceGateTiming, what string, check func() []string) error {
	deadline := time.Now().Add(timing.Timeout)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stopped while %s: %w", what, err)
		}
		problems := check()
		if len(problems) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: not healthy within %s: %s", what, timing.Timeout, strings.Join(problems, "; "))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stopped while %s (%s): %w", what, strings.Join(problems, "; "), ctx.Err())
		case <-time.After(timing.Interval):
		}
	}
}

// convergeGated converges one host through its role. When the precheck finds
// a change, gate must report no problems before the role changes the host and
// again after it, so the next host starts only once the service recovered. A
// converged host passes neither gate, and a dry run evaluates neither.
func (c *releaseHostConvergence) convergeGated(ctx context.Context, task *orchestrator.Task, dryRun bool, out io.Writer, timing serviceGateTiming, service string, gate func() []string) error {
	changed := false
	beforeChange := func() error {
		changed = true
		if dryRun {
			return nil
		}
		return pollServiceGate(ctx, timing, fmt.Sprintf("waiting for %s to be healthy before changing %s", service, task.Host), gate)
	}
	if err := c.convergeTaskBefore(ctx, task, dryRun, out, beforeChange); err != nil {
		return err
	}
	if !changed || dryRun {
		return nil
	}
	if err := pollServiceGate(ctx, timing, fmt.Sprintf("waiting for %s to recover after changing %s", service, task.Host), gate); err != nil {
		return err
	}
	fmt.Fprintf(out, "    %s healthy after changing %s\n", service, task.Host)
	return nil
}

// releaseServiceOps replaces the live service probes, restarts, and gate
// timeouts of the service stages in tests.
type releaseServiceOps struct {
	yugabyte   yugabyteReleaseOps
	kafka      kafkaReleaseOps
	clickhouse clickhouseReleaseOps
	timing     *serviceGateTiming
}

func (c *releaseHostConvergence) serviceTiming(defaults serviceGateTiming) serviceGateTiming {
	if c.serviceOps != nil && c.serviceOps.timing != nil {
		return *c.serviceOps.timing
	}
	return defaults
}
