package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
)

// Release Redis convergence applies the redis role to every server and
// Sentinel of each manifest Redis instance, so role fixes (masterauth, logging)
// reach running hosts without `cluster provision --force`. Each host runs the
// role's check-mode precheck first and is skipped when converged, and the role
// restarts a process only when its rendered config changed.
//
// A Sentinel-mode instance converges one host at a time in the order that
// keeps a primary serving: every server that is not the live primary (each
// must then report a synced replication link), then each Sentinel (each must
// then pass SENTINEL CKQUORUM), then the live primary. When the primary's
// precheck reports a change, a Sentinel failover moves the primary role to a
// synced replica before the role restarts it, and the demoted server must sync
// from the new primary afterwards. Roles are read from the live servers, not
// the manifest, because Sentinel may have moved the primary since provisioning.
// A single-mode instance converges in place.
const releaseHostStepRedis = "redis"

// redisTaskInstance is the manifest instance a planner Redis task belongs to.
func redisTaskInstance(task *orchestrator.Task) string {
	if label, ok := task.Metadata["instance_label"].(string); ok && label != "" {
		return label
	}
	return task.InstanceID
}

func redisTaskRole(task *orchestrator.Task) string {
	if role, ok := task.Metadata["redis_role"].(string); ok && role != "" {
		return role
	}
	return "primary"
}

func redisTaskGroupLabel(task *orchestrator.Task) string {
	if task.ClusterID != "" {
		return redisTaskInstance(task) + " (" + task.ClusterID + ")"
	}
	return redisTaskInstance(task)
}

// planRedisConvergenceSteps orders the planner's Redis tasks per instance:
// declared replicas, then Sentinels, then the declared primary, each group in
// host order. The live order is resolved at apply time.
func planRedisConvergenceSteps(tasks []*orchestrator.Task) []releaseHostConvergenceStep {
	rank := map[string]int{"replica": 0, "sentinel": 1, "primary": 2}
	sorted := append([]*orchestrator.Task(nil), tasks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		gi, gj := redisTaskGroupLabel(sorted[i]), redisTaskGroupLabel(sorted[j])
		if gi != gj {
			return gi < gj
		}
		ri, rj := rank[redisTaskRole(sorted[i])], rank[redisTaskRole(sorted[j])]
		if ri != rj {
			return ri < rj
		}
		return sorted[i].Host < sorted[j].Host
	})
	steps := make([]releaseHostConvergenceStep, 0, len(sorted))
	for _, task := range sorted {
		steps = append(steps, releaseHostConvergenceStep{Kind: releaseHostStepRedis, Label: task.Host, Group: redisTaskGroupLabel(task), Task: task})
	}
	return steps
}

// redisConvergenceGroup is one manifest Redis instance's tasks.
type redisConvergenceGroup struct {
	Label     string
	Primary   *orchestrator.Task
	Replicas  []*orchestrator.Task
	Sentinels []*orchestrator.Task
}

func (g redisConvergenceGroup) servers() []*orchestrator.Task {
	var servers []*orchestrator.Task
	if g.Primary != nil {
		servers = append(servers, g.Primary)
	}
	return append(servers, g.Replicas...)
}

func redisConvergenceGroups(steps []releaseHostConvergenceStep) []redisConvergenceGroup {
	var groups []redisConvergenceGroup
	index := map[string]int{}
	for _, step := range steps {
		if step.Kind != releaseHostStepRedis || step.Task == nil {
			continue
		}
		i, ok := index[step.Group]
		if !ok {
			i = len(groups)
			index[step.Group] = i
			groups = append(groups, redisConvergenceGroup{Label: step.Group})
		}
		switch redisTaskRole(step.Task) {
		case "replica":
			groups[i].Replicas = append(groups[i].Replicas, step.Task)
		case "sentinel":
			groups[i].Sentinels = append(groups[i].Sentinels, step.Task)
		default:
			groups[i].Primary = step.Task
		}
	}
	return groups
}

func taskHosts(tasks []*orchestrator.Task) []string {
	hosts := make([]string, 0, len(tasks))
	for _, task := range tasks {
		hosts = append(hosts, task.Host)
	}
	return hosts
}

// writeRedisConvergencePlan prints one line per Redis instance.
func writeRedisConvergencePlan(out io.Writer, steps []releaseHostConvergenceStep) {
	for _, g := range redisConvergenceGroups(steps) {
		if len(g.Sentinels) == 0 {
			fmt.Fprintf(out, "     · Redis %s: %s in place (restarts only when its config changes)\n", g.Label, strings.Join(taskHosts(g.servers()), ", "))
			continue
		}
		fmt.Fprintf(out, "     · Redis %s, one host at a time: servers %s except the live primary (each must sync), Sentinels %s (each must reach quorum), then the live primary after a Sentinel failover\n",
			g.Label, strings.Join(taskHosts(g.servers()), ", "), strings.Join(taskHosts(g.Sentinels), " -> "))
	}
}

// redisServerState is what a server reports in INFO replication.
type redisServerState struct {
	// Role is "master", "slave", or empty when the server did not answer.
	Role   string
	LinkUp bool
	// Detail says why the server did not answer.
	Detail string
}

func (s redisServerState) describe() string {
	switch {
	case s.Role == "":
		return "not answering: " + s.Detail
	case s.Role == "slave" && !s.LinkUp:
		return "replica with its replication link down"
	default:
		return "role " + s.Role
	}
}

// redisReleaseOps are the live Redis and Sentinel operations the convergence
// runs on a task's host.
type redisReleaseOps interface {
	ServerState(ctx context.Context, server *orchestrator.Task) (redisServerState, error)
	SentinelCheckQuorum(ctx context.Context, sentinel *orchestrator.Task) error
	// SentinelMaster returns the "host port" Sentinel reports as primary.
	SentinelMaster(ctx context.Context, sentinel *orchestrator.Task) (string, error)
	SentinelFailover(ctx context.Context, sentinel *orchestrator.Task) error
}

// redisGateTiming bounds the waits between hosts.
type redisGateTiming struct {
	Sync     time.Duration
	Quorum   time.Duration
	Failover time.Duration
	Interval time.Duration
}

var defaultRedisGateTiming = redisGateTiming{
	Sync:     5 * time.Minute,
	Quorum:   2 * time.Minute,
	Failover: 2 * time.Minute,
	Interval: 2 * time.Second,
}

func (c *releaseHostConvergence) redis() (redisReleaseOps, redisGateTiming) {
	timing := defaultRedisGateTiming
	if c.redisTiming != nil {
		timing = *c.redisTiming
	}
	if c.redisOps != nil {
		return c.redisOps, timing
	}
	return sshRedisReleaseOps{c: c}, timing
}

// runRedis converges every Redis instance in turn; a failure stops the stage
// with the remaining hosts untouched.
func (c *releaseHostConvergence) runRedis(ctx context.Context, steps []releaseHostConvergenceStep, dryRun bool) error {
	groups := redisConvergenceGroups(steps)
	out := c.cmd.OutOrStdout()
	for i, g := range groups {
		fmt.Fprintf(out, "\nRedis %s (%d/%d)\n", g.Label, i+1, len(groups))
		if err := c.convergeRedisGroup(ctx, g, dryRun, out); err != nil {
			return fmt.Errorf("redis %s: %w", g.Label, err)
		}
	}
	return nil
}

func (c *releaseHostConvergence) convergeRedisGroup(ctx context.Context, g redisConvergenceGroup, dryRun bool, out io.Writer) error {
	converge := func(task *orchestrator.Task, beforeChange func() error) error {
		fmt.Fprintf(out, "  %s %s on %s\n", redisTaskRole(task), redisTaskInstance(task), task.Host)
		if err := c.convergeTaskBefore(ctx, task, dryRun, out, beforeChange); err != nil {
			return fmt.Errorf("%s on %s: %w", redisTaskRole(task), task.Host, err)
		}
		return nil
	}
	if len(g.Sentinels) == 0 {
		for _, task := range g.servers() {
			if err := converge(task, nil); err != nil {
				return err
			}
		}
		return nil
	}

	ops, timing := c.redis()
	primary, err := redisLivePrimary(ctx, ops, g.servers())
	if err != nil {
		return err
	}
	livePrimary := primary != nil
	if livePrimary {
		fmt.Fprintf(out, "  live primary: %s\n", primary.Host)
	} else {
		// Nothing serves as primary, so there is nothing to fail over from
		// and no link to wait for; converging restores the processes.
		fmt.Fprintln(out, "  no server reports the primary role; converging in declared order without failover")
		primary = g.Primary
	}
	var others []*orchestrator.Task
	for _, task := range g.servers() {
		if task != primary {
			others = append(others, task)
		}
	}

	for _, task := range others {
		if err := converge(task, nil); err != nil {
			return err
		}
		if !dryRun && livePrimary {
			if err := waitRedisReplicaSynced(ctx, ops, timing, task); err != nil {
				return err
			}
		}
	}
	for _, sentinel := range g.Sentinels {
		if err := converge(sentinel, nil); err != nil {
			return err
		}
		if !dryRun {
			if err := waitRedisSentinelQuorum(ctx, ops, timing, sentinel); err != nil {
				return err
			}
		}
	}

	failedOver := false
	beforePrimaryChange := func() error {
		if !livePrimary || len(others) == 0 {
			return nil
		}
		if dryRun {
			fmt.Fprintf(out, "    [DRY-RUN] would fail the primary over to a synced replica through Sentinel before converging %s\n", primary.Host)
			return nil
		}
		if err := failoverRedisPrimary(ctx, ops, timing, g, primary, out); err != nil {
			return err
		}
		failedOver = true
		return nil
	}
	if err := converge(primary, beforePrimaryChange); err != nil {
		return err
	}
	if failedOver {
		return waitRedisReplicaSynced(ctx, ops, timing, primary)
	}
	return nil
}

// redisLivePrimary returns the one server reporting the master role, nil when
// none does, and an error when more than one does: converging then could
// restart the server clients still write to.
func redisLivePrimary(ctx context.Context, ops redisReleaseOps, servers []*orchestrator.Task) (*orchestrator.Task, error) {
	var masters []*orchestrator.Task
	for _, server := range servers {
		state, err := ops.ServerState(ctx, server)
		if err != nil {
			return nil, fmt.Errorf("read replication role on %s: %w", server.Host, err)
		}
		if state.Role == "master" {
			masters = append(masters, server)
		}
	}
	switch len(masters) {
	case 0:
		return nil, nil
	case 1:
		return masters[0], nil
	default:
		return nil, fmt.Errorf("servers on %s all report the primary role; resolve the split before converging", strings.Join(taskHosts(masters), ", "))
	}
}

// failoverRedisPrimary asks a Sentinel with quorum to fail over, then waits
// until Sentinel reports another primary and the old one runs as a replica.
func failoverRedisPrimary(ctx context.Context, ops redisReleaseOps, timing redisGateTiming, g redisConvergenceGroup, primary *orchestrator.Task, out io.Writer) error {
	var sentinel *orchestrator.Task
	var quorumErrs []error
	for _, candidate := range g.Sentinels {
		if err := ops.SentinelCheckQuorum(ctx, candidate); err != nil {
			quorumErrs = append(quorumErrs, fmt.Errorf("sentinel on %s: %w", candidate.Host, err))
			continue
		}
		sentinel = candidate
		break
	}
	if sentinel == nil {
		return fmt.Errorf("no Sentinel reaches quorum for a failover: %w", errors.Join(quorumErrs...))
	}
	previous, err := ops.SentinelMaster(ctx, sentinel)
	if err != nil {
		return fmt.Errorf("sentinel on %s: read primary: %w", sentinel.Host, err)
	}
	fmt.Fprintf(out, "    failing the primary over from %s through Sentinel on %s\n", primary.Host, sentinel.Host)
	if err = ops.SentinelFailover(ctx, sentinel); err != nil {
		return fmt.Errorf("sentinel on %s: failover: %w", sentinel.Host, err)
	}
	err = pollRedisGate(ctx, timing.Failover, timing.Interval, func() (bool, string) {
		current, masterErr := ops.SentinelMaster(ctx, sentinel)
		if masterErr != nil {
			return false, "Sentinel primary unreadable: " + masterErr.Error()
		}
		if current == previous {
			return false, "Sentinel still reports " + previous
		}
		state, stateErr := ops.ServerState(ctx, primary)
		if stateErr != nil {
			return false, "old primary unreadable: " + stateErr.Error()
		}
		if state.Role != "slave" {
			return false, "old primary " + state.describe()
		}
		return true, ""
	})
	if err != nil {
		return fmt.Errorf("failover from %s: %w", primary.Host, err)
	}
	return nil
}

func waitRedisReplicaSynced(ctx context.Context, ops redisReleaseOps, timing redisGateTiming, server *orchestrator.Task) error {
	err := pollRedisGate(ctx, timing.Sync, timing.Interval, func() (bool, string) {
		state, err := ops.ServerState(ctx, server)
		if err != nil {
			return false, err.Error()
		}
		return state.Role == "slave" && state.LinkUp, state.describe()
	})
	if err != nil {
		return fmt.Errorf("replica on %s did not sync with the primary: %w", server.Host, err)
	}
	return nil
}

func waitRedisSentinelQuorum(ctx context.Context, ops redisReleaseOps, timing redisGateTiming, sentinel *orchestrator.Task) error {
	err := pollRedisGate(ctx, timing.Quorum, timing.Interval, func() (bool, string) {
		if err := ops.SentinelCheckQuorum(ctx, sentinel); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
	if err != nil {
		return fmt.Errorf("sentinel on %s did not reach quorum: %w", sentinel.Host, err)
	}
	return nil
}

// pollRedisGate re-evaluates check until it passes or timeout elapses, and
// reports the last status on timeout.
func pollRedisGate(ctx context.Context, timeout, interval time.Duration, check func() (bool, string)) error {
	deadline := time.Now().Add(timeout)
	for {
		ok, status := check()
		if ok {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not reached within %s (last: %s)", timeout, status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// sshRedisReleaseOps runs valkey-cli on the task's host over SSH.
type sshRedisReleaseOps struct {
	c *releaseHostConvergence
}

// run returns the CLI output; answered is false when the command ran but the
// CLI failed (the server did not answer), with its stderr as the reply. argsFor
// builds the CLI arguments from the task's rendered config.
func (o sshRedisReleaseOps) run(ctx context.Context, task *orchestrator.Task, argsFor func(provisioner.ServiceConfig) []string) (reply string, answered bool, err error) {
	host, ok := o.c.manifest.GetHost(task.Host)
	if !ok {
		return "", false, fmt.Errorf("host %s not found in manifest", task.Host)
	}
	config, err := buildTaskConfig(task, o.c.manifest, o.c.runtimeData, false, o.c.manifestDir, o.c.sharedEnv, o.c.clusterEnvs, o.c.releaseRepos)
	if err != nil {
		return "", false, err
	}
	result, err := o.c.pool.Run(ctx, sshConfigFor(host), provisioner.RedisCLICommand(config, argsFor(config)...))
	if result != nil && result.ExitCode > 0 {
		return strings.TrimSpace(result.Stderr + " " + result.Stdout), false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.ReplaceAll(result.Stdout, "\r", ""), true, nil
}

func (o sshRedisReleaseOps) ServerState(ctx context.Context, server *orchestrator.Task) (redisServerState, error) {
	reply, answered, err := o.run(ctx, server, func(provisioner.ServiceConfig) []string {
		return []string{"INFO", "replication"}
	})
	if err != nil || !answered {
		return redisServerState{Detail: reply}, err
	}
	return parseRedisReplicationInfo(reply), nil
}

// parseRedisReplicationInfo reads INFO replication. An error reply (NOAUTH,
// LOADING) has no role line and reads as not answering.
func parseRedisReplicationInfo(reply string) redisServerState {
	var state redisServerState
	for line := range strings.SplitSeq(reply, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		switch key {
		case "role":
			state.Role = value
		case "master_link_status":
			state.LinkUp = value == "up"
		}
	}
	if state.Role == "" {
		state.Detail = strings.TrimSpace(reply)
	}
	return state
}

func (o sshRedisReleaseOps) sentinel(ctx context.Context, sentinel *orchestrator.Task, command string) (string, error) {
	reply, answered, err := o.run(ctx, sentinel, func(config provisioner.ServiceConfig) []string {
		master := ""
		if name, ok := config.Metadata["redis_master_name"].(string); ok {
			master = name
		}
		return []string{"SENTINEL", command, master}
	})
	if err != nil {
		return "", err
	}
	if !answered {
		return "", fmt.Errorf("sentinel not answering: %s", reply)
	}
	return strings.TrimSpace(reply), nil
}

func (o sshRedisReleaseOps) SentinelCheckQuorum(ctx context.Context, sentinel *orchestrator.Task) error {
	reply, err := o.sentinel(ctx, sentinel, "CKQUORUM")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(reply, "OK") {
		return errors.New(reply)
	}
	return nil
}

func (o sshRedisReleaseOps) SentinelMaster(ctx context.Context, sentinel *orchestrator.Task) (string, error) {
	reply, err := o.sentinel(ctx, sentinel, "get-master-addr-by-name")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(reply)
	if len(fields) != 2 {
		return "", fmt.Errorf("unexpected reply %q", reply)
	}
	return fields[0] + " " + fields[1], nil
}

func (o sshRedisReleaseOps) SentinelFailover(ctx context.Context, sentinel *orchestrator.Task) error {
	reply, err := o.sentinel(ctx, sentinel, "FAILOVER")
	if err != nil {
		return err
	}
	if reply != "OK" {
		return errors.New(reply)
	}
	return nil
}
