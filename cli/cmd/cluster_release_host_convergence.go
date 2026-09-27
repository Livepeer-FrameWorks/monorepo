package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"sync"

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
//   - The node baseline (operator tools, journald and logrotate retention,
//     key-only sshd, fail2ban) and OS tuning on every planned host, in
//     criticality waves; a new SSH login to each host of a wave must succeed
//     before the next wave starts. Only sshd reloads and journald restarts,
//     and only on hosts whose config changed.
//   - Privateer on every mesh host (binary, seed peers, seed DNS). Seeds carry
//     the names both the previous and the target release dial, so converging
//     them first breaks no running service.
//   - Every Redis server and Sentinel, keeping a primary serving throughout
//     (cluster_release_redis_convergence.go).
//   - The stateful data services and managed dependencies, each host alone
//     and gated on the service's health (cluster_release_service_convergence.go).
//   - The declared Kafka topics, created when missing, with their declared
//     topic config (retention) applied to existing topics.
//   - MirrorMaker2 workers on every declared link worker host, so each
//     region's mirrored topics exist before Signalman and Bridge roll.
//
// Every release runs the stage. Each host step renders the desired role state
// and runs the role's check-mode precheck first, so an unchanged host is
// skipped without a restart and a rerun after a failure resumes where the
// cluster diverges. Privateer hosts converge in criticality waves
// (release_rollout_waves.go): a CONTROL canary alone, the other CONTROL hosts
// one at a time, MEDIA hosts one per cell with cells in parallel, then OTHER
// hosts together; mesh health is verified on each wave's hosts before the next
// wave starts, and a failed wave leaves every later host untouched. MirrorMaker2 workers converge their config
// one at a time without restarting; every worker left with a pending restart
// then restarts together, so the workers of a target never run mixed configs
// through a sequence of rebalances.
const (
	releaseHostStepNodeBaseline = "node-baseline"
	releaseHostStepPrivateer    = "privateer"
	releaseHostStepKafkaTopics  = "kafka-topics"
	releaseHostStepMirrorMaker  = "kafka-mirrormaker"
)

// releaseHostConvergenceStep is one ordered step of the stage. Task is nil for
// a node baseline step, whose Label is the host, and for the Kafka topic step,
// which runs against each cluster's first broker. Group names the Redis
// instance a Redis step belongs to.
type releaseHostConvergenceStep struct {
	Kind  string
	Label string
	Group string
	Task  *orchestrator.Task
}

// planReleaseHostConvergence derives the stage from the execution plan and the
// manifest: every planned host's node baseline, then Privateer hosts, then
// Redis instances, then the data services and managed dependencies, then the
// Kafka clusters that declare topics, then MirrorMaker2 worker hosts. It
// performs no I/O.
func planReleaseHostConvergence(plan *orchestrator.ExecutionPlan, manifest *inventory.Manifest) []releaseHostConvergenceStep {
	var privateer, redis, mirrorMaker []*orchestrator.Task
	if plan != nil {
		for _, task := range plan.AllTasks {
			switch {
			case task == nil:
			case task.Phase == orchestrator.PhaseMesh && task.Type == "privateer":
				privateer = append(privateer, task)
			case task.Type == releaseHostStepRedis:
				redis = append(redis, task)
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
	for _, host := range plannedProvisionHosts(plan) {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepNodeBaseline, Label: host})
	}
	for _, task := range privateer {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepPrivateer, Label: task.Host, Task: task})
	}
	steps = append(steps, planRedisConvergenceSteps(redis)...)
	steps = append(steps, planReleaseServiceSteps(plan)...)
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

// releasePrivateerWaves orders the Privateer steps' hosts by the risk of the
// component being converged, not by what else runs on each host: Privateer's
// DNS socket is systemd-activated and wg0 survives its restart, so restarting
// it barely affects co-located services. The most critical host goes first as
// a canary, then the rest in batches with a mesh-health check after each batch.
// Pure over the manifest.
func releasePrivateerWaves(manifest *inventory.Manifest, steps []releaseHostConvergenceStep) []rolloutWave {
	var hosts []string
	for _, step := range steps {
		if step.Kind == releaseHostStepPrivateer && step.Task != nil {
			hosts = append(hosts, step.Task.Host)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	tiers := rolloutHostTiers(manifest)
	return buildComponentBatchWaves(hosts, func(host string) rolloutTier { return tiers[host] }, privateerConvergenceBatch)
}

// privateerConvergenceBatch bounds how many mesh hosts restart Privateer at
// once after the canary.
const privateerConvergenceBatch = 4

// releaseNodeBaselineWaves orders the node baseline hosts the way Privateer
// hosts are ordered: the most critical host alone as a canary, then batches.
// A bad sshd render is caught by the role's sshd -t check; the waves bound what
// anything it misses can reach before the SSH login gate sees it.
func releaseNodeBaselineWaves(manifest *inventory.Manifest, steps []releaseHostConvergenceStep) []rolloutWave {
	var hosts []string
	for _, step := range steps {
		if step.Kind == releaseHostStepNodeBaseline {
			hosts = append(hosts, step.Label)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	tiers := rolloutHostTiers(manifest)
	return buildComponentBatchWaves(hosts, func(host string) rolloutTier { return tiers[host] }, nodeBaselineConvergenceBatch)
}

// nodeBaselineConvergenceBatch bounds how many hosts converge their node
// baseline at once after the canary.
const nodeBaselineConvergenceBatch = 4

// writeReleaseHostConvergencePlan prints the stage: the Privateer waves, then
// one line per remaining step kind.
func writeReleaseHostConvergencePlan(out io.Writer, heading string, manifest *inventory.Manifest, steps []releaseHostConvergenceStep) {
	if len(steps) == 0 {
		fmt.Fprintf(out, "  %s: none\n", heading)
		return
	}
	fmt.Fprintf(out, "  %s (hosts already converged are skipped):\n", heading)
	if waves := releaseNodeBaselineWaves(manifest, steps); len(waves) > 0 {
		fmt.Fprintln(out, "     · Node baseline and OS tuning (tools, journald/logrotate retention, key-only sshd, fail2ban), in waves (a new SSH login to each host gates each wave; only sshd reloads and journald restarts):")
		for i, wave := range waves {
			fmt.Fprintf(out, "         %d. %s\n", i+1, wave.describe())
		}
	}
	if waves := releasePrivateerWaves(manifest, steps); len(waves) > 0 {
		fmt.Fprintln(out, "     · Privateer binary, seed peers, and seed DNS, in waves (mesh health gates each wave):")
		for i, wave := range waves {
			fmt.Fprintf(out, "         %d. %s\n", i+1, wave.describe())
		}
	}
	writeRedisConvergencePlan(out, steps)
	writeReleaseServiceConvergencePlan(out, steps)
	groups := map[string][]string{}
	for _, step := range steps {
		groups[step.Kind] = append(groups[step.Kind], step.Label)
	}
	for _, group := range []struct{ kind, label, sep string }{
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

	// mirrorMakerRestartPendingFn and mirrorMakerRestartFn replace the SSH
	// probe and the role restart in tests.
	mirrorMakerRestartPendingFn func(context.Context, *orchestrator.Task) (bool, error)
	mirrorMakerRestartFn        func(context.Context, *orchestrator.Task) error
	// verifyMeshFn replaces the SSH mesh health gate in tests.
	verifyMeshFn func(context.Context, []string) error
	// nodeHostFn and verifySSHFn replace the node baseline convergence and the
	// SSH login gate in tests.
	nodeHostFn  func(ctx context.Context, host inventory.Host, dryRun bool, out io.Writer) error
	verifySSHFn func(context.Context, []string) error
	// redisOps and redisTiming replace the SSH Redis operations and the gate
	// timeouts in tests.
	redisOps    redisReleaseOps
	redisTiming *redisGateTiming
	// serviceOps replaces the live operations of the service stages in tests.
	serviceOps *releaseServiceOps
}

func (c *releaseHostConvergence) verifyMesh(ctx context.Context, hosts []string) error {
	if c.verifyMeshFn != nil {
		return c.verifyMeshFn(ctx, hosts)
	}
	return verifyMeshHealth(ctx, c.cmd, c.manifest, c.pool, hosts)
}

// restartPendingMirrorMakers restarts, concurrently, every MirrorMaker2 worker
// among tasks whose role recorded a deferred restart. The marker lives on the
// host, so a worker converged by an interrupted earlier run is restarted too.
func (c *releaseHostConvergence) restartPendingMirrorMakers(ctx context.Context, tasks []*orchestrator.Task, dryRun bool) error {
	if len(tasks) == 0 {
		return nil
	}
	out := c.cmd.OutOrStdout()
	if dryRun {
		fmt.Fprintln(out, "\n[DRY-RUN] MirrorMaker2 workers with changed config or binaries would restart together after every worker converged")
		return nil
	}
	pendingFn := c.mirrorMakerRestartPendingFn
	if pendingFn == nil {
		pendingFn = c.mirrorMakerRestartPending
	}
	restartFn := c.mirrorMakerRestartFn
	if restartFn == nil {
		restartFn = c.restartMirrorMaker
	}
	var pending []*orchestrator.Task
	var hosts []string
	for _, task := range tasks {
		isPending, err := pendingFn(ctx, task)
		if err != nil {
			return fmt.Errorf("kafka-mirrormaker on %s: probe pending restart: %w", task.Host, err)
		}
		if isPending {
			pending = append(pending, task)
			hosts = append(hosts, task.Host)
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(out, "\nMirrorMaker2 workers: no restart pending")
		return nil
	}
	fmt.Fprintf(out, "\nRestarting MirrorMaker2 workers together: %s\n", strings.Join(hosts, ", "))
	errs := make([]error, len(pending))
	var wg sync.WaitGroup
	for i, task := range pending {
		wg.Go(func() {
			if err := restartFn(ctx, task); err != nil {
				errs[i] = fmt.Errorf("kafka-mirrormaker on %s: restart: %w", task.Host, err)
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	ux.Success(out, fmt.Sprintf("Restarted MirrorMaker2 workers: %s", strings.Join(hosts, ", ")))
	return nil
}

func (c *releaseHostConvergence) mirrorMakerRestartPending(ctx context.Context, task *orchestrator.Task) (bool, error) {
	host, ok := c.manifest.GetHost(task.Host)
	if !ok {
		return false, fmt.Errorf("host %s not found in manifest", task.Host)
	}
	result, err := c.pool.Run(ctx, sshConfigFor(host), provisioner.KafkaMirrorMakerRestartPendingProbe)
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("probe exited %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return strings.TrimSpace(result.Stdout) == "PENDING", nil
}

// restartMirrorMaker runs the role's restart tag, which clears the marker,
// and validates the restarted worker.
func (c *releaseHostConvergence) restartMirrorMaker(ctx context.Context, task *orchestrator.Task) error {
	host, ok := c.manifest.GetHost(task.Host)
	if !ok {
		return fmt.Errorf("host %s not found in manifest", task.Host)
	}
	prov, config, err := renderProvisionTask(task, c.pool, c.manifest, false, c.runtimeData, c.manifestDir, c.sharedEnv, c.clusterEnvs, c.releaseRepos)
	if err != nil {
		return err
	}
	restarter, ok := prov.(provisioner.Restarter)
	if !ok {
		return fmt.Errorf("%s provisioner cannot restart", prov.GetName())
	}
	if err := restarter.Restart(ctx, host, config); err != nil {
		return err
	}
	return runProvisionPhase(ctx, provisionValidateTimeout, "validate", func(phaseCtx context.Context) error {
		return prov.Validate(phaseCtx, host, config)
	})
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
	runtimeData, err := rc.RuntimeData(cmd.Context(), stringFlag(cmd, "ssh-key").Value)
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

// run executes (or, with dryRun, previews) the stage: the Privateer waves,
// each gated on the mesh health of its hosts, then the Redis instances, then
// the remaining steps in order.
func (c *releaseHostConvergence) run(ctx context.Context, steps []releaseHostConvergenceStep, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	if err := c.runNodeBaselineWaves(ctx, steps, dryRun); err != nil {
		return err
	}
	if err := c.runPrivateerWaves(ctx, steps, dryRun); err != nil {
		return err
	}
	if err := c.runRedis(ctx, steps, dryRun); err != nil {
		return err
	}
	if err := c.runReleaseServices(ctx, steps, dryRun); err != nil {
		return err
	}
	var mirrorMakers []*orchestrator.Task
	for i, step := range steps {
		switch step.Kind {
		case releaseHostStepNodeBaseline, releaseHostStepPrivateer, releaseHostStepRedis, releaseHostStepService:
			continue
		case releaseHostStepMirrorMaker:
			fmt.Fprintf(out, "\n[host %d/%d] %s on %s\n", i+1, len(steps), step.Kind, step.Label)
			mirrorMakers = append(mirrorMakers, step.Task)
			if err := c.convergeTask(ctx, step.Task, dryRun, out); err != nil {
				err = fmt.Errorf("%s on %s: %w", step.Kind, step.Label, err)
				// Workers converged so far carry a deferred restart; leaving
				// them on their old config until a rerun would keep the
				// mixed state the deferral exists to avoid.
				if restartErr := c.restartPendingMirrorMakers(ctx, mirrorMakers, dryRun); restartErr != nil {
					return errors.Join(err, restartErr)
				}
				return err
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
	return c.restartPendingMirrorMakers(ctx, mirrorMakers, dryRun)
}

// runNodeBaselineWaves converges each planned host's node baseline and OS
// tuning wave by wave. Before the next wave starts, a new SSH login must
// succeed on every host of the wave just converged, so a baseline that locks
// the operator out stops the rollout at its canary.
func (c *releaseHostConvergence) runNodeBaselineWaves(ctx context.Context, steps []releaseHostConvergenceStep, dryRun bool) error {
	waves := releaseNodeBaselineWaves(c.manifest, steps)
	if len(waves) == 0 {
		return nil
	}
	out := c.cmd.OutOrStdout()
	fmt.Fprintf(out, "\nNode baseline and OS tuning on %d host(s) in %d wave(s)\n", len(releaseNodeBaselineHosts(waves)), len(waves))
	converge := c.nodeHostFn
	if converge == nil {
		converge = func(ctx context.Context, host inventory.Host, dryRun bool, out io.Writer) error {
			return convergeNodeHost(ctx, out, c.pool, c.manifest, host, false, dryRun)
		}
	}
	verify := c.verifySSHFn
	if verify == nil {
		verify = func(ctx context.Context, hosts []string) error {
			return verifyFreshSSHLogin(ctx, c.pool, c.manifest, hosts)
		}
	}
	err := runRolloutWaves(ctx, out, waves,
		func(ctx context.Context, name string, hostOut io.Writer) error {
			host, ok := c.manifest.GetHost(name)
			if !ok {
				return fmt.Errorf("host %s not found in manifest", name)
			}
			fmt.Fprintf(hostOut, "node baseline on %s\n", name)
			return converge(ctx, host, dryRun, hostOut)
		},
		func(wave rolloutWave) error {
			if dryRun {
				return nil
			}
			return verify(ctx, wave.hosts())
		})
	if err != nil {
		return fmt.Errorf("node baseline: %w", err)
	}
	return nil
}

func releaseNodeBaselineHosts(waves []rolloutWave) []string {
	var hosts []string
	for _, wave := range waves {
		hosts = append(hosts, wave.hosts()...)
	}
	return hosts
}

// runPrivateerWaves converges the Privateer hosts wave by wave. Before the
// next wave starts, the hosts of the wave just converged must pass the mesh
// health gate, so a canary that breaks the mesh stops the rollout on one host.
func (c *releaseHostConvergence) runPrivateerWaves(ctx context.Context, steps []releaseHostConvergenceStep, dryRun bool) error {
	waves := releasePrivateerWaves(c.manifest, steps)
	if len(waves) == 0 {
		return nil
	}
	tasks := map[string]*orchestrator.Task{}
	for _, step := range steps {
		if step.Kind == releaseHostStepPrivateer && step.Task != nil {
			tasks[step.Task.Host] = step.Task
		}
	}
	out := c.cmd.OutOrStdout()
	fmt.Fprintf(out, "\nPrivateer on %d host(s) in %d wave(s)\n", len(tasks), len(waves))
	err := runRolloutWaves(ctx, out, waves,
		func(ctx context.Context, host string, hostOut io.Writer) error {
			fmt.Fprintf(hostOut, "privateer on %s\n", host)
			return c.convergeTask(ctx, tasks[host], dryRun, hostOut)
		},
		func(wave rolloutWave) error {
			if dryRun {
				return nil
			}
			if err := c.verifyMesh(ctx, wave.hosts()); err != nil {
				return fmt.Errorf("mesh health after Privateer convergence: %w", err)
			}
			return nil
		})
	if err != nil {
		return fmt.Errorf("privateer: %w", err)
	}
	return nil
}

// convergeTask runs one host step through provisionTask, which detects the
// installed state, skips an unchanged host via the role precheck, and validates
// what it changed. With dryRun it reports to out what provisionTask would do.
// Each call renders from its own copy of the runtime data, since Privateer
// hosts in one wave converge concurrently.
func (c *releaseHostConvergence) convergeTask(ctx context.Context, task *orchestrator.Task, dryRun bool, out io.Writer) error {
	return c.convergeTaskBefore(ctx, task, dryRun, out, nil)
}

// convergeTaskBefore is convergeTask with beforeChange run right before the
// role changes the host, or, with dryRun, where it would; its error aborts
// the step. A host the precheck finds converged never calls it.
func (c *releaseHostConvergence) convergeTaskBefore(ctx context.Context, task *orchestrator.Task, dryRun bool, out io.Writer, beforeChange func() error) error {
	host, ok := c.manifest.GetHost(task.Host)
	if !ok {
		return fmt.Errorf("host %s not found in manifest", task.Host)
	}
	runtimeData := maps.Clone(c.runtimeData)
	if runtimeData == nil {
		runtimeData = map[string]any{}
	}
	if task.Type == releaseHostStepMirrorMaker {
		runtimeData[provisioner.KafkaMirrorMakerDeferRestartKey] = true
	}
	maps.Copy(runtimeData, releaseRuntimeOverrides(task))
	if !dryRun {
		_, err := provisionTask(ctx, task, host, c.pool, c.manifest, false, false, runtimeData, c.manifestDir, c.sharedEnv, c.clusterEnvs, c.releaseRepos, beforeChange)
		return err
	}
	prov, config, err := renderProvisionTask(task, c.pool, c.manifest, false, runtimeData, c.manifestDir, c.sharedEnv, c.clusterEnvs, c.releaseRepos)
	if err != nil {
		return err
	}
	if missing := missingRequiredExternalEnv(task.Type, config.EnvVars); len(missing) > 0 {
		return requiredEnvPreflightError([]requiredEnvGap{{Target: fmt.Sprintf("%s on %s", task.Name, task.Host), Missing: missing}})
	}
	state := detectProvisionTaskState(ctx, prov, host, config)
	switch {
	case !serviceExists(state):
		fmt.Fprintln(out, "    [DRY-RUN] would install")
	case provisionNeededWithoutPrecheck(state, config.DeferStart):
		fmt.Fprintln(out, "    [DRY-RUN] installed but not running; would provision to restore it")
	default:
		inspection, checkErr := provisionInspectChanges(ctx, prov, host, config, nil)
		if checkErr != nil {
			return fmt.Errorf("precheck: %w", checkErr)
		}
		if !inspection.Changed {
			ux.Success(out, "    already converged; would skip")
			return nil
		}
		fmt.Fprintln(out, "    [DRY-RUN] would converge (role check reports changes; the service restarts)")
		writeProvisionChangeTasks(out, "      ", inspection.Tasks)
	}
	if beforeChange != nil {
		return beforeChange()
	}
	return nil
}
