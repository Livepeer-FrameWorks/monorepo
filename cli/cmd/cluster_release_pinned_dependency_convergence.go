package cmd

import (
	"context"
	"io"
	"sort"

	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// Release-pinned managed dependencies are the managed dependencies whose
// artifact the platform release manifest itself pins under
// external_dependencies (the Livepeer gateway and signer install go-livepeer).
// A release that moves that pin changes the binary these hosts must run, so
// `release apply` converges them, after the platform services they call
// (Foghorn's auth webhook, Decklog telemetry) run the target release, the
// same ordering the edge rollout follows. Each host runs the go_service role
// precheck against the target release and is skipped when its installed
// artifact already matches; a changed host converges alone and the role's
// validation (Livepeer health and keystore checks) must pass before the next
// host starts.

// releasePinnedDependencyTypes orders the stage: the signer before the
// gateways that may sign through it.
var releasePinnedDependencyTypes = []string{"livepeer-signer", "livepeer-gateway"}

// planReleasePinnedDependencies returns the planner tasks of the release-pinned
// managed dependencies, by cluster, then type order, then host. It performs no
// I/O.
func planReleasePinnedDependencies(plan *orchestrator.ExecutionPlan) []*orchestrator.Task {
	if plan == nil {
		return nil
	}
	rank := map[string]int{}
	for i, taskType := range releasePinnedDependencyTypes {
		rank[taskType] = i
	}
	var tasks []*orchestrator.Task
	for _, task := range plan.AllTasks {
		if task == nil {
			continue
		}
		if _, ok := rank[task.Type]; !ok {
			continue
		}
		if servicedefs.DeliveryClassFor(task.Type) != servicedefs.DeliveryManagedDependency || provisioner.ExternalDependencyForService(task.Type) == "" {
			continue
		}
		tasks = append(tasks, task)
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
	return tasks
}

func describeReleasePinnedDependencies(out io.Writer, tasks []*orchestrator.Task) {
	if len(tasks) == 0 {
		return
	}
	describeDependencyConvergence(out, "then the release-pinned managed dependencies (go-livepeer from the release manifest; role validated)", tasks)
}

// releasePinnedDependencySteps wraps the tasks as host steps for the env
// contract preflight.
func releasePinnedDependencySteps(tasks []*orchestrator.Task) []releaseHostConvergenceStep {
	steps := make([]releaseHostConvergenceStep, 0, len(tasks))
	for _, task := range tasks {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepService, Label: task.Host, Group: "release-pinned dependencies", Task: task})
	}
	return steps
}

func (c *releaseHostConvergence) runReleasePinnedDependencies(ctx context.Context, tasks []*orchestrator.Task, dryRun bool) error {
	if len(tasks) == 0 {
		return nil
	}
	return c.runDependencyConvergence(ctx, "Release-pinned managed dependencies", tasks, dryRun)
}
