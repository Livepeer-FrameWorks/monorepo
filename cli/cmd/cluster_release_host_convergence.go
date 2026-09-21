package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"frameworks/cli/internal/ux"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// Release host convergence runs before the first migration or service upgrade.
// It converges the host-level substrate the upgraded services resolve and
// consume, through the same provisioner paths `cluster provision` uses:
//
//   - Privateer on every mesh host (binary, seed peers, seed DNS). Seeds carry
//     the names both the previous and the target release dial, so converging
//     them first breaks no running service.
//   - The declared Kafka topics, created when missing, with their declared
//     topic config (retention) applied to existing topics. Brokers and
//     controllers are never reconfigured or restarted.
//   - MirrorMaker2 workers on every declared link worker host, so each
//     region's mirrored topics exist before Signalman and Bridge roll.
//
// Every release runs the stage. Each host step renders the desired role state
// and runs the role's check-mode precheck first, so an unchanged host is
// skipped without a restart and a rerun after a failure resumes where the
// cluster diverges. Hosts converge one at a time: a Privateer restart briefly
// interrupts that host's mesh DNS, and a MirrorMaker2 restart rebalances its
// connector tasks onto the remaining workers.
const (
	releaseHostStepPrivateer   = "privateer"
	releaseHostStepKafkaTopics = "kafka-topics"
	releaseHostStepMirrorMaker = "kafka-mirrormaker"
)

// releaseHostConvergenceStep is one ordered step of the stage. Task is nil for
// the Kafka topic step, which runs against each cluster's first broker.
type releaseHostConvergenceStep struct {
	Kind  string
	Label string
	Task  *orchestrator.Task
}

// planReleaseHostConvergence derives the stage from the execution plan and the
// manifest: Privateer hosts, then the Kafka clusters that declare topics, then
// MirrorMaker2 worker hosts, each group in host order. It performs no I/O.
func planReleaseHostConvergence(plan *orchestrator.ExecutionPlan, manifest *inventory.Manifest) []releaseHostConvergenceStep {
	var privateer, mirrorMaker []*orchestrator.Task
	if plan != nil {
		for _, task := range plan.AllTasks {
			switch {
			case task == nil:
			case task.Phase == orchestrator.PhaseMesh && task.Type == "privateer":
				privateer = append(privateer, task)
			case task.Type == "kafka-mirrormaker":
				mirrorMaker = append(mirrorMaker, task)
			}
		}
	}
	byHost := func(tasks []*orchestrator.Task) {
		sort.Slice(tasks, func(i, j int) bool { return tasks[i].Host < tasks[j].Host })
	}
	byHost(privateer)
	byHost(mirrorMaker)

	var steps []releaseHostConvergenceStep
	for _, task := range privateer {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepPrivateer, Label: task.Host, Task: task})
	}
	var topicClusters []string
	clusters := allKafkaClusters(manifest)
	for i := range clusters {
		if len(clusters[i].Topics) > 0 {
			topicClusters = append(topicClusters, kafkaClusterAlias(manifest, &clusters[i]))
		}
	}
	if len(topicClusters) > 0 {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepKafkaTopics, Label: strings.Join(topicClusters, ", ")})
	}
	for _, task := range mirrorMaker {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepMirrorMaker, Label: task.Host, Task: task})
	}
	return steps
}

// writeReleaseHostConvergencePlan prints the stage as one line per step kind.
func writeReleaseHostConvergencePlan(out io.Writer, heading string, steps []releaseHostConvergenceStep) {
	if len(steps) == 0 {
		fmt.Fprintf(out, "  %s: none\n", heading)
		return
	}
	fmt.Fprintf(out, "  %s (hosts already converged are skipped):\n", heading)
	groups := map[string][]string{}
	for _, step := range steps {
		groups[step.Kind] = append(groups[step.Kind], step.Label)
	}
	for _, group := range []struct{ kind, label, sep string }{
		{releaseHostStepPrivateer, "Privateer binary, seed peers, and seed DNS", " -> "},
		{releaseHostStepKafkaTopics, "Kafka topics created when missing, topic config applied (no broker restart)", ", "},
		{releaseHostStepMirrorMaker, "MirrorMaker2 workers and JMX exporter", " -> "},
	} {
		if labels := groups[group.kind]; len(labels) > 0 {
			fmt.Fprintf(out, "     · %s: %s\n", group.label, strings.Join(labels, group.sep))
		}
	}
}

// releaseHostConvergence carries what every step renders with: the manifest
// pinned to the concrete release and the provision runtime data.
type releaseHostConvergence struct {
	cmd          *cobra.Command
	manifest     *inventory.Manifest
	pool         *ssh.Pool
	runtimeData  map[string]any
	manifestDir  string
	sharedEnv    map[string]string
	clusterEnvs  map[string]map[string]string
	releaseRepos []string
}

func newReleaseHostConvergence(cmd *cobra.Command, rc *resolvedCluster, platformVersion string, pool *ssh.Pool) (*releaseHostConvergence, error) {
	frozen := *rc.Manifest
	frozen.Channel = platformVersion
	manifestDir := filepath.Dir(rc.ManifestPath)
	sharedEnv, err := rc.PreparedSharedEnv()
	if err != nil {
		return nil, fmt.Errorf("load manifest env_files: %w", err)
	}
	clusterEnvs, err := rc.ClusterEnvs()
	if err != nil {
		return nil, fmt.Errorf("load cluster env_files: %w", err)
	}
	runtimeData, err := provisionRuntimeData(&frozen, manifestDir, sharedEnv)
	if err != nil {
		return nil, err
	}
	return &releaseHostConvergence{
		cmd:          cmd,
		manifest:     &frozen,
		pool:         pool,
		runtimeData:  runtimeData,
		manifestDir:  manifestDir,
		sharedEnv:    sharedEnv,
		clusterEnvs:  clusterEnvs,
		releaseRepos: rc.ReleaseRepos,
	}, nil
}

// preflightEnvContract renders every host step's task as convergeTask
// deploys it and reports every schema-contract failure before any host
// changes, so a bad env refuses the release instead of stopping it after
// earlier hosts converged.
func (c *releaseHostConvergence) preflightEnvContract(steps []releaseHostConvergenceStep) error {
	var failures envContractFailures
	for _, step := range steps {
		task := step.Task
		if task == nil || task.Phase == orchestrator.PhaseInfrastructure {
			continue
		}
		config, err := buildTaskConfig(task, c.manifest, c.runtimeData, false, c.manifestDir, c.sharedEnv, c.clusterEnvs, c.releaseRepos)
		if err != nil {
			return fmt.Errorf("%s on %s: render for env preflight: %w", step.Kind, step.Label, err)
		}
		host, _ := c.manifest.GetHost(task.Host)
		failures.check(c.manifest, fmt.Sprintf("%s on %s", task.Name, task.Host), task.Type, provisioner.FinalServiceEnv(task.Type, host, config))
	}
	return failures.err()
}

// run executes (or, with dryRun, previews) every step in order. The mesh is
// verified once every Privateer host has converged and before Kafka work
// starts.
func (c *releaseHostConvergence) run(ctx context.Context, steps []releaseHostConvergenceStep, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	var meshHosts []string
	for i, step := range steps {
		if step.Kind != releaseHostStepPrivateer && len(meshHosts) > 0 && !dryRun {
			if err := verifyMeshHealth(ctx, c.cmd, c.manifest, c.pool, meshHosts); err != nil {
				return fmt.Errorf("mesh health after Privateer convergence: %w", err)
			}
			meshHosts = nil
		}
		switch step.Kind {
		case releaseHostStepPrivateer, releaseHostStepMirrorMaker:
			fmt.Fprintf(out, "\n[host %d/%d] %s on %s\n", i+1, len(steps), step.Kind, step.Label)
			if err := c.convergeTask(ctx, step.Task, dryRun); err != nil {
				return fmt.Errorf("%s on %s: %w", step.Kind, step.Label, err)
			}
			if step.Kind == releaseHostStepPrivateer {
				meshHosts = append(meshHosts, step.Task.Host)
			}
		case releaseHostStepKafkaTopics:
			fmt.Fprintf(out, "\n[host %d/%d] Kafka topics for %s\n", i+1, len(steps), step.Label)
			if dryRun {
				fmt.Fprintln(out, "    [DRY-RUN] would create any declared topic that does not exist and apply declared topic config that differs")
				continue
			}
			if err := initializeDeferredKafka(ctx, c.cmd, c.manifest, c.pool, c.releaseRepos); err != nil {
				return fmt.Errorf("kafka topics: %w", err)
			}
		default:
			return fmt.Errorf("unknown host convergence step %q", step.Kind)
		}
	}
	if len(meshHosts) > 0 && !dryRun {
		if err := verifyMeshHealth(ctx, c.cmd, c.manifest, c.pool, meshHosts); err != nil {
			return fmt.Errorf("mesh health after Privateer convergence: %w", err)
		}
	}
	return nil
}

// convergeTask runs one host step through provisionTask, which detects the
// installed state, skips an unchanged host via the role precheck, and validates
// what it changed. With dryRun it reports what provisionTask would do.
func (c *releaseHostConvergence) convergeTask(ctx context.Context, task *orchestrator.Task, dryRun bool) error {
	host, ok := c.manifest.GetHost(task.Host)
	if !ok {
		return fmt.Errorf("host %s not found in manifest", task.Host)
	}
	if !dryRun {
		_, err := provisionTask(ctx, task, host, c.pool, c.manifest, false, false, c.runtimeData, c.manifestDir, c.sharedEnv, c.clusterEnvs, c.releaseRepos)
		return err
	}
	prov, config, err := renderProvisionTask(task, c.pool, c.manifest, false, c.runtimeData, c.manifestDir, c.sharedEnv, c.clusterEnvs, c.releaseRepos)
	if err != nil {
		return err
	}
	if missing := missingRequiredExternalEnv(task.Type, config.EnvVars); len(missing) > 0 {
		return requiredEnvPreflightError([]requiredEnvGap{{Target: fmt.Sprintf("%s on %s", task.Name, task.Host), Missing: missing}})
	}
	out := c.cmd.OutOrStdout()
	state := detectProvisionTaskState(ctx, prov, host, config)
	switch {
	case !serviceExists(state):
		fmt.Fprintln(out, "    [DRY-RUN] would install")
	case provisionNeededWithoutPrecheck(state, config.DeferStart):
		fmt.Fprintln(out, "    [DRY-RUN] installed but not running; would provision to restore it")
	default:
		wouldChange, checkErr := provisionWouldChange(ctx, prov, host, config, nil)
		if checkErr != nil {
			return fmt.Errorf("precheck: %w", checkErr)
		}
		if wouldChange {
			fmt.Fprintln(out, "    [DRY-RUN] would converge (role check reports changes; the service restarts)")
		} else {
			ux.Success(out, "    already converged; would skip")
		}
	}
	return nil
}
