package cmd

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func yugabyteReleaseManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile: "dev",
		Hosts:   map[string]inventory.Host{"yb-1": serviceHost("yb-1"), "yb-2": serviceHost("yb-2"), "yb-3": serviceHost("yb-3")},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true, Engine: "yugabyte", Mode: "native",
				Nodes: []inventory.PostgresNode{{Host: "yb-1", ID: 1}, {Host: "yb-2", ID: 2}, {Host: "yb-3", ID: 3}},
			},
		},
	}
}

// fakeYugabyteOps drives the stage against a fakeUniverse: drift names the
// nodes whose tserver-scope precheck reports a change, and a tserver restart
// brings its node's tserver current.
type fakeYugabyteOps struct {
	log      *serviceEventLog
	universe *fakeUniverse
	drift    map[string]bool
}

func (o *fakeYugabyteOps) Roll(io.Writer) *yugabyteRoll {
	return o.universe.roll("yb-1", "yb-2", "yb-3")
}

func (o *fakeYugabyteOps) TserverConfigDrift(_ context.Context, node *orchestrator.Task) (bool, error) {
	return o.drift[node.Host], nil
}

func (o *fakeYugabyteOps) RestartTserver(_ context.Context, node *orchestrator.Task) error {
	o.log.add("restart-tserver:" + node.Host)
	o.universe.mu.Lock()
	o.universe.processes[node.Host+"/yb-tserver"] = yugabyteProcessCurrent
	o.universe.mu.Unlock()
	o.universe.recover(node.Host)
	return nil
}

func runYugabyteReleaseConvergence(t *testing.T, ops *fakeYugabyteOps, converged map[string]bool, dryRun bool) serviceConvergenceRun {
	t.Helper()
	fake := &serviceReleaseProvisioner{log: ops.log, converged: converged}
	return runServiceConvergence(t, yugabyteReleaseManifest(), fake, &releaseServiceOps{yugabyte: ops}, dryRun)
}

// Masters converge first, each only after the masters confirm it can go down;
// then only the tservers whose applied config differs restart, one at a time.
func TestReleaseYugabyteConvergenceRollsMastersThenStaleTservers(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), processes: map[string]yugabyteProcessState{"yb-1/yb-tserver": yugabyteProcessStale}}
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe, drift: map[string]bool{"yb-3": true}}
	run := runYugabyteReleaseConvergence(t, ops, map[string]bool{"yugabyte[master]:yb-2": true}, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{
		"provision:yugabyte[master]:yb-1",
		"provision:yugabyte[master]:yb-3",
		"restart-tserver:yb-1",
		"restart-tserver:yb-3",
	}
	if !slices.Equal(run.events, want) {
		t.Fatalf("events = %v\nwant     %v", run.events, want)
	}
	// Two master changes ask about the master alone, two tserver restarts
	// about the whole node; the converged master is never asked about.
	var masterAsks, nodeAsks int
	for _, asked := range universe.asked {
		switch {
		case len(asked) == 1 && asked[0] == "m-yb-2":
			t.Fatalf("converged master yb-2 was gated: %v", universe.asked)
		case len(asked) == 1:
			masterAsks++
		case slices.Contains(asked, "ts-yb-1") || slices.Contains(asked, "ts-yb-3"):
			nodeAsks++
		}
	}
	if masterAsks != 2 || nodeAsks < 2 {
		t.Fatalf("masters asked %v; want each changed master and each restarted node admitted", universe.asked)
	}
}

func TestReleaseYugabyteConvergenceStopsWhenTheMastersRefuse(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), unsafeCalls: -1}
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "master on yb-1") || !strings.Contains(run.err.Error(), "would lose its leader") {
		t.Fatalf("err = %v, want the first master refused", run.err)
	}
	if len(run.events) != 0 {
		t.Fatalf("a node changed without the masters' consent: %v", run.events)
	}
}

func TestReleaseYugabyteConvergenceSkipsAConvergedUniverse(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), processes: map[string]yugabyteProcessState{}}
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	converged := map[string]bool{"yugabyte[master]:yb-1": true, "yugabyte[master]:yb-2": true, "yugabyte[master]:yb-3": true}
	run := runYugabyteReleaseConvergence(t, ops, converged, false)
	if run.err != nil || len(run.events) != 0 || len(universe.asked) != 0 {
		t.Fatalf("converged universe: err %v, events %v, asked %v", run.err, run.events, universe.asked)
	}
	if !strings.Contains(run.out, "every tserver runs its applied configuration") {
		t.Fatalf("output:\n%s", run.out)
	}
}

func TestReleaseYugabyteConvergenceDryRunNeitherChangesNorProbes(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll()}
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, nil, true)
	if run.err != nil || len(run.events) != 0 || universe.probes != 0 || len(universe.asked) != 0 {
		t.Fatalf("dry-run: err %v, events %v, probes %d", run.err, run.events, universe.probes)
	}
	if strings.Count(run.out, "[DRY-RUN] would converge") != 3 {
		t.Fatalf("dry-run output:\n%s", run.out)
	}
}

func TestReleaseYugabyteConvergencePlanLine(t *testing.T) {
	manifest := yugabyteReleaseManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, planReleaseHostConvergence(plan, manifest))
	if !strings.Contains(out.String(), "· Yugabyte, one node at a time: masters of yb-1 -> yb-2 -> yb-3") {
		t.Fatalf("plan output:\n%s", out.String())
	}
}
