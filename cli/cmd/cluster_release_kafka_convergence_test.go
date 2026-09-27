package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// kafkaReleaseManifest declares three controllers (kc-1..3, ids 1-3) and three
// brokers (kb-1..3, ids 11-13) in one Kafka cluster.
func kafkaReleaseManifest() *inventory.Manifest {
	hosts := map[string]inventory.Host{}
	for _, name := range []string{"kc-1", "kc-2", "kc-3", "kb-1", "kb-2", "kb-3"} {
		hosts[name] = serviceHost(name)
	}
	return &inventory.Manifest{
		Profile: "dev",
		Hosts:   hosts,
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled: true, Mode: "native",
				Controllers: []inventory.KafkaController{{Host: "kc-1", ID: 1}, {Host: "kc-2", ID: 2}, {Host: "kc-3", ID: 3}},
				Brokers:     []inventory.KafkaBroker{{Host: "kb-1", ID: 11}, {Host: "kb-2", ID: 12}, {Host: "kb-3", ID: 13}},
			},
		},
	}
}

// fakeKafkaCluster answers health reads for one cluster. A changed host
// reports as not caught up for the next lagReads reads; hosts in broken never
// recover once changed.
type fakeKafkaCluster struct {
	log      *serviceEventLog
	mu       sync.Mutex
	lagReads int
	pending  map[string]int
	broken   map[string]bool
	changed  map[string]bool
	urp      []string
}

func (f *fakeKafkaCluster) afterProvision(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	host := key[strings.LastIndex(key, ":")+1:]
	if f.pending == nil {
		f.pending = map[string]int{}
		f.changed = map[string]bool{}
	}
	f.pending[host] = f.lagReads
	f.changed[host] = true
}

func (f *fakeKafkaCluster) Health(_ context.Context, broker *orchestrator.Task) (kafkaReleaseHealth, error) {
	f.log.add("health:" + broker.Host)
	f.mu.Lock()
	defer f.mu.Unlock()
	health := kafkaReleaseHealth{UnderReplicated: slices.Clone(f.urp)}
	for i, host := range []string{"kc-1", "kc-2", "kc-3"} {
		voter := kafkaQuorumVoter{NodeID: i + 1, Status: "Follower"}
		if i == 0 {
			voter.Status = "Leader"
		}
		if f.pending[host] > 0 || (f.broken[host] && f.changed[host]) {
			voter.Lag = 5000
		}
		health.Voters = append(health.Voters, voter)
	}
	for _, host := range []string{"kb-1", "kb-2", "kb-3"} {
		if f.pending[host] > 0 || (f.broken[host] && f.changed[host]) {
			health.UnderReplicated = append(health.UnderReplicated, "analytics_events/0")
		}
	}
	for host, reads := range f.pending {
		if reads > 0 {
			f.pending[host] = reads - 1
		}
	}
	return health, nil
}

func runKafkaReleaseConvergence(t *testing.T, cluster *fakeKafkaCluster, converged map[string]bool, dryRun bool) serviceConvergenceRun {
	t.Helper()
	fake := &serviceReleaseProvisioner{log: cluster.log, converged: converged, afterProvision: cluster.afterProvision}
	return runServiceConvergence(t, kafkaReleaseManifest(), fake, &releaseServiceOps{kafka: cluster}, dryRun)
}

func TestReleaseKafkaConvergencePlansControllersThenBrokers(t *testing.T) {
	manifest := kafkaReleaseManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	steps := planReleaseHostConvergence(plan, manifest)
	var got []string
	for _, step := range steps {
		got = append(got, step.Kind+":"+step.Group+":"+step.Task.Type+":"+step.Label)
	}
	want := []string{
		"service:kafka:kafka-controller:kc-1", "service:kafka:kafka-controller:kc-2", "service:kafka:kafka-controller:kc-3",
		"service:kafka:kafka:kb-1", "service:kafka:kafka:kb-2", "service:kafka:kafka:kb-3",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("steps = %v\nwant    %v", got, want)
	}
	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, steps)
	if !strings.Contains(out.String(), "· Kafka primary, one host at a time, each gated on a quorum leader with every controller caught up and no under-replicated or unavailable partition: controllers kc-1 -> kc-2 -> kc-3, then brokers kb-1 -> kb-2 -> kb-3") {
		t.Fatalf("plan output:\n%s", out.String())
	}
}

// Every changed host is gated on a healthy cluster before its role runs and
// waits for the cluster to recover before the next host; a converged host is
// neither changed nor gated.
func TestReleaseKafkaConvergenceRollsOneHostAtATimeBehindHealthGates(t *testing.T) {
	cluster := &fakeKafkaCluster{log: &serviceEventLog{}, lagReads: 2}
	run := runKafkaReleaseConvergence(t, cluster, map[string]bool{"kafka-controller:kc-2": true, "kafka:kb-1": true}, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	var provisions []string
	for _, event := range run.events {
		if strings.HasPrefix(event, "provision:") {
			provisions = append(provisions, event)
		}
	}
	wantProvisions := []string{"provision:kafka-controller:kc-1", "provision:kafka-controller:kc-3", "provision:kafka:kb-2", "provision:kafka:kb-3"}
	if !slices.Equal(provisions, wantProvisions) {
		t.Fatalf("provisions = %v, want %v", provisions, wantProvisions)
	}
	// Each change reads health once before (healthy) and, after it, until
	// the host has caught up: two lagging reads and one healthy one.
	for _, changed := range []string{"provision:kafka-controller:kc-1", "provision:kafka:kb-2"} {
		i := slices.Index(run.events, changed)
		if i < 1 || !strings.HasPrefix(run.events[i-1], "health:") {
			t.Fatalf("%s was not gated on cluster health first: %v", changed, run.events)
		}
		after := run.events[i+1 : i+4]
		for _, event := range after {
			if !strings.HasPrefix(event, "health:") {
				t.Fatalf("%s: next host started before the cluster recovered: %v", changed, run.events)
			}
		}
	}
	// A broker's health is read through another broker while it restarts.
	i := slices.Index(run.events, "provision:kafka:kb-2")
	if run.events[i+1] == "health:kb-2" {
		t.Fatalf("health after changing kb-2 was read through kb-2 itself: %v", run.events)
	}
}

func TestReleaseKafkaConvergenceStopsWhenAHostDoesNotRecover(t *testing.T) {
	cluster := &fakeKafkaCluster{log: &serviceEventLog{}, broken: map[string]bool{"kc-2": true}}
	run := runKafkaReleaseConvergence(t, cluster, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "controller 2 trails the quorum leader by 5000 records") || !strings.Contains(run.err.Error(), "controller on kc-2") {
		t.Fatalf("err = %v, want a quorum gate failure after kc-2", run.err)
	}
	for _, event := range run.events {
		if event == "provision:kafka-controller:kc-3" || strings.HasPrefix(event, "provision:kafka:") {
			t.Fatalf("a host after kc-2 was changed: %v", run.events)
		}
	}
}

func TestReleaseKafkaConvergenceRefusesToChangeAnUnhealthyCluster(t *testing.T) {
	cluster := &fakeKafkaCluster{log: &serviceEventLog{}, urp: []string{"service_events/2"}}
	run := runKafkaReleaseConvergence(t, cluster, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "before changing kc-1") || !strings.Contains(run.err.Error(), "1 under-replicated partition(s) (service_events/2)") {
		t.Fatalf("err = %v, want the pre-change gate to refuse", run.err)
	}
	for _, event := range run.events {
		if strings.HasPrefix(event, "provision:") {
			t.Fatalf("a host changed while partitions were under-replicated: %v", run.events)
		}
	}
}

func TestReleaseKafkaConvergenceDryRunNeitherChangesNorProbes(t *testing.T) {
	cluster := &fakeKafkaCluster{log: &serviceEventLog{}}
	run := runKafkaReleaseConvergence(t, cluster, map[string]bool{"kafka:kb-3": true}, true)
	if run.err != nil {
		t.Fatalf("dry-run: %v", run.err)
	}
	if len(run.events) != 0 {
		t.Fatalf("dry-run acted: %v", run.events)
	}
	if strings.Count(run.out, "[DRY-RUN] would converge") != 5 || !strings.Contains(run.out, "already converged; would skip") {
		t.Fatalf("dry-run output:\n%s", run.out)
	}
}

func TestReleaseKafkaConvergenceReadsThroughAnotherBrokerWhenOneIsDown(t *testing.T) {
	ops := &failingKafkaOps{down: "kb-2"}
	cluster := kafkaConvergenceCluster{Label: "primary", Controllers: []*orchestrator.Task{{Host: "kc-1"}}, Brokers: []*orchestrator.Task{{Host: "kb-1"}, {Host: "kb-2"}}}
	if problems := kafkaClusterProblems(context.Background(), ops, cluster, cluster.Brokers[0]); len(problems) != 1 || !strings.Contains(problems[0], "no broker answered") {
		t.Fatalf("problems = %v, want no broker answering (kb-2 down, kb-1 is the target and answers last)", problems)
	}
	if !slices.Equal(ops.asked, []string{"kb-2", "kb-1"}) {
		t.Fatalf("asked %v, want the other broker first", ops.asked)
	}
}

type failingKafkaOps struct {
	down  string
	asked []string
}

func (o *failingKafkaOps) Health(_ context.Context, broker *orchestrator.Task) (kafkaReleaseHealth, error) {
	o.asked = append(o.asked, broker.Host)
	return kafkaReleaseHealth{}, errors.New("connection refused")
}

// Output of kafka-metadata-quorum describe --replication and kafka-topics
// --describe from Kafka 4.2.
func TestParseKafkaReleaseHealth(t *testing.T) {
	out := "== quorum\n" +
		"NodeId\tDirectoryId           \tLogEndOffset\tLag\tLastFetchTimestamp\tLastCaughtUpTimestamp\tStatus\t\n" +
		"1     \tAAAAAAAAAAAAAAAAAAAAAA\t19          \t0  \t1790545103065     \t1790545103065        \tLeader\t\n" +
		"2     \tBBBBBBBBBBBBBBBBBBBBBB\t12          \t7  \t1790545103065     \t1790545103065        \tFollower\t\n" +
		"11    \tCCCCCCCCCCCCCCCCCCCCCC\t19          \t0  \t1790545103065     \t1790545103065        \tObserver\t\n" +
		"== under-replicated\n" +
		"\tTopic: t1\tPartition: 1\tLeader: 1\tReplicas: 1,2\tIsr: 1\tElr: \tLastKnownElr: \n" +
		"== unavailable\n"
	health, err := parseKafkaReleaseHealth(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []kafkaQuorumVoter{{NodeID: 1, Lag: 0, Status: "Leader"}, {NodeID: 2, Lag: 7, Status: "Follower"}}
	if !slices.Equal(health.Voters, want) || !slices.Equal(health.UnderReplicated, []string{"t1/1"}) || len(health.Unavailable) != 0 {
		t.Fatalf("health = %+v", health)
	}
	if problems := health.problems(3); !slices.Equal(problems, []string{"2 of 3 controllers in the quorum", "1 under-replicated partition(s) (t1/1)"}) {
		t.Fatalf("problems = %v", problems)
	}
	if _, err := parseKafkaReleaseHealth("Error: connection refused"); err == nil {
		t.Fatal("output without a quorum table must not read as healthy")
	}
}
