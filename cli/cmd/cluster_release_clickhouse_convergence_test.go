package cmd

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func clickhouseReleaseManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile: "dev",
		Hosts:   map[string]inventory.Host{"ch-1": serviceHost("ch-1")},
		Infrastructure: inventory.InfrastructureConfig{
			ClickHouse: &inventory.ClickHouseConfig{Enabled: true, Mode: "native", Nodes: []inventory.ClickHouseNode{{Host: "ch-1", ID: 1}}},
		},
	}
}

// fakeClickHouse reports problems for the next lagReads reads after a node
// changes, forever when broken, and always when unhealthy is set.
type fakeClickHouse struct {
	log       *serviceEventLog
	mu        sync.Mutex
	lagReads  int
	pending   int
	broken    bool
	changed   bool
	unhealthy []string
}

func (f *fakeClickHouse) afterProvision(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = f.lagReads
	f.changed = true
}

func (f *fakeClickHouse) Problems(_ context.Context, node *orchestrator.Task) ([]string, error) {
	f.log.add("health:" + node.Host)
	f.mu.Lock()
	defer f.mu.Unlock()
	problems := slices.Clone(f.unhealthy)
	if f.pending > 0 || (f.broken && f.changed) {
		problems = append(problems, "replica periscope.viewer_sessions read_only=1 session_expired=1 delay=0s parts_to_fetch=0 is not caught up")
	}
	if f.pending > 0 {
		f.pending--
	}
	return problems, nil
}

func runClickHouseReleaseConvergence(t *testing.T, ch *fakeClickHouse, converged map[string]bool, dryRun bool) serviceConvergenceRun {
	t.Helper()
	fake := &serviceReleaseProvisioner{log: ch.log, converged: converged, afterProvision: ch.afterProvision}
	return runServiceConvergence(t, clickhouseReleaseManifest(), fake, &releaseServiceOps{clickhouse: ch}, dryRun)
}

func TestReleaseClickHouseConvergenceGatesTheChangedNode(t *testing.T) {
	ch := &fakeClickHouse{log: &serviceEventLog{}, lagReads: 2}
	run := runClickHouseReleaseConvergence(t, ch, nil, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{"health:ch-1", "provision:clickhouse:ch-1", "health:ch-1", "health:ch-1", "health:ch-1"}
	if !slices.Equal(run.events, want) {
		t.Fatalf("events = %v, want %v", run.events, want)
	}

	var out bytes.Buffer
	manifest := clickhouseReleaseManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, planReleaseHostConvergence(plan, manifest), "")
	if !strings.Contains(out.String(), "· ClickHouse, one node at a time, each gated on every node reaching Keeper with its replicas caught up: ch-1") {
		t.Fatalf("plan output:\n%s", out.String())
	}
}

func TestReleaseClickHouseConvergenceSkipsAConvergedNode(t *testing.T) {
	ch := &fakeClickHouse{log: &serviceEventLog{}, unhealthy: []string{"Keeper unreachable: connection refused"}}
	run := runClickHouseReleaseConvergence(t, ch, map[string]bool{"clickhouse:ch-1": true}, false)
	if run.err != nil || len(run.events) != 0 {
		t.Fatalf("converged node: err %v, events %v; want neither a change nor a gate", run.err, run.events)
	}
}

func TestReleaseClickHouseConvergenceStopsWhenTheNodeDoesNotRecover(t *testing.T) {
	ch := &fakeClickHouse{log: &serviceEventLog{}, broken: true}
	run := runClickHouseReleaseConvergence(t, ch, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "recover after changing ch-1") || !strings.Contains(run.err.Error(), "periscope.viewer_sessions") {
		t.Fatalf("err = %v, want a replica gate failure", run.err)
	}
}

func TestReleaseClickHouseConvergenceRefusesWithoutKeeper(t *testing.T) {
	ch := &fakeClickHouse{log: &serviceEventLog{}, unhealthy: []string{"Keeper unreachable: connection refused"}}
	run := runClickHouseReleaseConvergence(t, ch, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "before changing ch-1") || !strings.Contains(run.err.Error(), "ch-1: Keeper unreachable") {
		t.Fatalf("err = %v, want the pre-change gate to refuse", run.err)
	}
	if slices.Contains(run.events, "provision:clickhouse:ch-1") {
		t.Fatalf("node changed without Keeper: %v", run.events)
	}
}

func TestReleaseClickHouseConvergenceDryRunNeitherChangesNorProbes(t *testing.T) {
	ch := &fakeClickHouse{log: &serviceEventLog{}}
	run := runClickHouseReleaseConvergence(t, ch, nil, true)
	if run.err != nil || len(run.events) != 0 || !strings.Contains(run.out, "[DRY-RUN] would converge") {
		t.Fatalf("dry-run: err %v, events %v, output:\n%s", run.err, run.events, run.out)
	}
}

func TestParseClickHouseReleaseHealth(t *testing.T) {
	problems, err := parseClickHouseReleaseHealth("keeper ok\n")
	if err != nil || len(problems) != 0 {
		t.Fatalf("healthy: %v %v", problems, err)
	}
	problems, err = parseClickHouseReleaseHealth("keeper unreachable: Code: 999. Coordination::Exception: Connection loss\nreplica periscope.t read_only=1 session_expired=1 delay=0s parts_to_fetch=0\n")
	if err != nil || !slices.Equal(problems, []string{
		"Keeper unreachable: Code: 999. Coordination::Exception: Connection loss",
		"replica periscope.t read_only=1 session_expired=1 delay=0s parts_to_fetch=0 is not caught up",
	}) {
		t.Fatalf("unhealthy: %v %v", problems, err)
	}
	if _, err := parseClickHouseReleaseHealth("sh: clickhouse-client: not found"); err == nil {
		t.Fatal("a report without the Keeper line must not read as healthy")
	}
}
