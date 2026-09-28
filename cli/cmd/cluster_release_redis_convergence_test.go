package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// redisReleaseManifest declares a Sentinel-mode foghorn instance (primary
// redis-a, replicas redis-b and redis-c, a Sentinel on each) and a single-mode
// platform instance on redis-a.
func redisReleaseManifest() *inventory.Manifest {
	host := func(name, ip string) inventory.Host {
		return inventory.Host{Name: name, ExternalIP: ip, WireguardIP: ip}
	}
	return &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"redis-a": host("redis-a", "10.88.0.1"),
			"redis-b": host("redis-b", "10.88.0.2"),
			"redis-c": host("redis-c", "10.88.0.3"),
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{
						Name:         "foghorn",
						Mode:         "sentinel",
						Host:         "redis-a",
						Port:         6380,
						ReplicaHosts: []string{"redis-b", "redis-c"},
						Sentinels:    []inventory.RedisSentinelNode{{Host: "redis-a"}, {Host: "redis-b"}, {Host: "redis-c"}},
					},
					{Name: "platform", Host: "redis-a", Port: 6379},
				},
			},
		},
	}
}

func redisConvergenceSteps(t *testing.T, manifest *inventory.Manifest) []releaseHostConvergenceStep {
	t.Helper()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var steps []releaseHostConvergenceStep
	for _, step := range planReleaseHostConvergence(plan, manifest) {
		if step.Kind != releaseHostStepNodeBaseline {
			steps = append(steps, step)
		}
	}
	return steps
}

func TestReleaseHostConvergencePlansRedisReplicasSentinelsThenPrimary(t *testing.T) {
	manifest := redisReleaseManifest()
	steps := redisConvergenceSteps(t, manifest)
	var got []string
	for _, step := range steps {
		got = append(got, step.Kind+":"+step.Group+":"+redisTaskRole(step.Task)+":"+step.Label)
	}
	want := []string{
		"redis:foghorn:replica:redis-b",
		"redis:foghorn:replica:redis-c",
		"redis:foghorn:sentinel:redis-a",
		"redis:foghorn:sentinel:redis-b",
		"redis:foghorn:sentinel:redis-c",
		"redis:foghorn:primary:redis-a",
		"redis:platform:primary:redis-a",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("redis steps = %v, want %v", got, want)
	}

	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, steps, "")
	for _, line := range []string{
		"· Redis foghorn, one host at a time: servers redis-a, redis-b, redis-c except the live primary (each must sync), Sentinels redis-a -> redis-b -> redis-c (each must reach quorum), then the live primary after a Sentinel failover",
		"· Redis platform: redis-a in place (restarts only when its config changes)",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("plan output missing %q:\n%s", line, out.String())
		}
	}
}

// redisReleaseProvisioner is a redis role whose precheck reports drift on
// every task except those in converged ("<role>:<host>"), and which records
// each apply.
type redisReleaseProvisioner struct {
	fakeTaskProvisioner
	log       *redisReleaseLog
	converged map[string]bool
}

func redisConfigKey(host inventory.Host, config provisioner.ServiceConfig) string {
	role, _ := config.Metadata["redis_role"].(string)
	instance, _ := config.Metadata["instance"].(string)
	return instance + "/" + role + ":" + host.Name
}

func (p *redisReleaseProvisioner) Detect(context.Context, inventory.Host) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: true, Running: true}, nil
}

func (p *redisReleaseProvisioner) WouldChange(_ context.Context, host inventory.Host, config provisioner.ServiceConfig, tags []string) (bool, error) {
	if len(tags) > 0 {
		return false, nil
	}
	return !p.converged[redisConfigKey(host, config)], nil
}

func (p *redisReleaseProvisioner) Provision(_ context.Context, host inventory.Host, config provisioner.ServiceConfig) error {
	p.log.add("provision:" + redisConfigKey(host, config))
	return p.log.provisionErr
}

type redisReleaseLog struct {
	mu           sync.Mutex
	events       []string
	provisionErr error
}

func (l *redisReleaseLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *redisReleaseLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// fakeRedisCluster answers the live Redis operations for one Sentinel set.
// Servers named in unsynced never report a replication link; a failover moves
// the primary role to the first synced replica in host order.
type fakeRedisCluster struct {
	log      *redisReleaseLog
	roles    map[string]string
	unsynced map[string]bool
}

func (f *fakeRedisCluster) ServerState(_ context.Context, server *orchestrator.Task) (redisServerState, error) {
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	role := f.roles[server.Host]
	return redisServerState{Role: role, LinkUp: role == "slave" && !f.unsynced[server.Host], Detail: "connection refused"}, nil
}

func (f *fakeRedisCluster) SentinelCheckQuorum(_ context.Context, sentinel *orchestrator.Task) error {
	f.log.add("quorum:" + sentinel.Host)
	return nil
}

func (f *fakeRedisCluster) SentinelMaster(context.Context, *orchestrator.Task) (string, error) {
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	for _, host := range []string{"redis-a", "redis-b", "redis-c"} {
		if f.roles[host] == "master" {
			return host + " 6380", nil
		}
	}
	return "", errors.New("no primary")
}

func (f *fakeRedisCluster) SentinelFailover(_ context.Context, sentinel *orchestrator.Task) error {
	f.log.mu.Lock()
	defer f.log.mu.Unlock()
	f.log.events = append(f.log.events, "failover:"+sentinel.Host)
	var old, next string
	for _, host := range []string{"redis-a", "redis-b", "redis-c"} {
		switch {
		case f.roles[host] == "master":
			old = host
		case next == "" && f.roles[host] == "slave" && !f.unsynced[host]:
			next = host
		}
	}
	if old == "" || next == "" {
		return errors.New("NOGOODSLAVE")
	}
	f.roles[old], f.roles[next] = "slave", "master"
	return nil
}

type redisConvergenceRun struct {
	events []string
	out    string
	err    error
}

func runRedisConvergence(t *testing.T, cluster *fakeRedisCluster, converged map[string]bool, dryRun bool) redisConvergenceRun {
	t.Helper()
	manifest := redisReleaseManifest()
	steps := redisConvergenceSteps(t, manifest)
	fake := &redisReleaseProvisioner{log: cluster.log, converged: converged}
	original := taskProvisioner
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { taskProvisioner = original })

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	c := &releaseHostConvergence{
		cmd:         cmd,
		manifest:    manifest,
		runtimeData: map[string]any{},
		sharedEnv:   map[string]string{},
		redisOps:    cluster,
		redisTiming: &redisGateTiming{Sync: 50 * time.Millisecond, Quorum: 50 * time.Millisecond, Failover: 50 * time.Millisecond, Interval: time.Millisecond},
	}
	err := c.run(context.Background(), steps, dryRun)
	return redisConvergenceRun{events: cluster.log.snapshot(), out: out.String(), err: err}
}

// Sentinel moved the primary to redis-b after provisioning: the stage must
// follow the live role, not the manifest's declared primary.
func TestReleaseRedisConvergenceFailsOverBeforeConvergingTheLivePrimary(t *testing.T) {
	log := &redisReleaseLog{}
	cluster := &fakeRedisCluster{log: log, roles: map[string]string{"redis-a": "slave", "redis-b": "master", "redis-c": "slave"}}
	run := runRedisConvergence(t, cluster, nil, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{
		"provision:foghorn/primary:redis-a",
		"provision:foghorn/replica:redis-c",
		"provision:foghorn/sentinel:redis-a",
		"quorum:redis-a",
		"provision:foghorn/sentinel:redis-b",
		"quorum:redis-b",
		"provision:foghorn/sentinel:redis-c",
		"quorum:redis-c",
		"quorum:redis-a",
		"failover:redis-a",
		"provision:foghorn/replica:redis-b",
		"provision:platform/primary:redis-a",
	}
	if !slices.Equal(run.events, want) {
		t.Fatalf("events = %v\nwant     %v\n%s", run.events, want, run.out)
	}
	if cluster.roles["redis-b"] != "slave" || cluster.roles["redis-a"] != "master" {
		t.Fatalf("roles after convergence = %v, want redis-b demoted", cluster.roles)
	}
	if !strings.Contains(run.out, "live primary: redis-b") {
		t.Fatalf("output does not name the live primary:\n%s", run.out)
	}
}

// A primary whose role already matches is not restarted, so no failover runs.
func TestReleaseRedisConvergenceSkipsFailoverForConvergedPrimary(t *testing.T) {
	log := &redisReleaseLog{}
	cluster := &fakeRedisCluster{log: log, roles: map[string]string{"redis-a": "master", "redis-b": "slave", "redis-c": "slave"}}
	run := runRedisConvergence(t, cluster, map[string]bool{"foghorn/primary:redis-a": true}, false)
	if run.err != nil {
		t.Fatalf("run: %v", run.err)
	}
	for _, event := range run.events {
		if strings.HasPrefix(event, "failover:") || event == "provision:foghorn/primary:redis-a" {
			t.Fatalf("converged primary was failed over or re-provisioned: %v", run.events)
		}
	}
	if cluster.roles["redis-a"] != "master" {
		t.Fatalf("roles = %v, want redis-a still primary", cluster.roles)
	}
}

// A replica that never syncs stops the stage before the primary is touched,
// and a rerun resumes from the live state.
func TestReleaseRedisConvergenceStopsWhenAReplicaDoesNotSync(t *testing.T) {
	log := &redisReleaseLog{}
	cluster := &fakeRedisCluster{log: log, roles: map[string]string{"redis-a": "master", "redis-b": "slave", "redis-c": "slave"}, unsynced: map[string]bool{"redis-b": true}}
	run := runRedisConvergence(t, cluster, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "replica on redis-b did not sync") || !strings.Contains(run.err.Error(), "replication link down") {
		t.Fatalf("err = %v, want a sync gate failure on redis-b", run.err)
	}
	if !slices.Equal(run.events, []string{"provision:foghorn/replica:redis-b"}) {
		t.Fatalf("events = %v, want only redis-b converged", run.events)
	}
}

func TestReleaseRedisConvergenceRefusesTwoPrimaries(t *testing.T) {
	log := &redisReleaseLog{}
	cluster := &fakeRedisCluster{log: log, roles: map[string]string{"redis-a": "master", "redis-b": "master", "redis-c": "slave"}}
	run := runRedisConvergence(t, cluster, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "redis-a, redis-b all report the primary role") {
		t.Fatalf("err = %v, want a split-primary refusal", run.err)
	}
	if len(run.events) != 0 {
		t.Fatalf("events = %v, want nothing converged", run.events)
	}
}

func TestReleaseRedisConvergenceDryRunReportsFailoverWithoutActing(t *testing.T) {
	log := &redisReleaseLog{}
	cluster := &fakeRedisCluster{log: log, roles: map[string]string{"redis-a": "master", "redis-b": "slave", "redis-c": "slave"}}
	run := runRedisConvergence(t, cluster, nil, true)
	if run.err != nil {
		t.Fatalf("dry-run: %v", run.err)
	}
	if len(run.events) != 0 {
		t.Fatalf("dry-run acted: %v", run.events)
	}
	if !strings.Contains(run.out, "[DRY-RUN] would fail the primary over to a synced replica through Sentinel before converging redis-a") {
		t.Fatalf("dry-run output does not report the failover:\n%s", run.out)
	}
}

func TestParseRedisReplicationInfo(t *testing.T) {
	replica := parseRedisReplicationInfo("# Replication\nrole:slave\nmaster_host:10.88.0.1\nmaster_link_status:down\n")
	if replica.Role != "slave" || replica.LinkUp {
		t.Fatalf("replica = %+v", replica)
	}
	if got := parseRedisReplicationInfo("role:slave\nmaster_link_status:up\n"); !got.LinkUp {
		t.Fatalf("synced replica = %+v", got)
	}
	if got := parseRedisReplicationInfo("NOAUTH Authentication required."); got.Role != "" || got.Detail != "NOAUTH Authentication required." {
		t.Fatalf("error reply = %+v", got)
	}
}
