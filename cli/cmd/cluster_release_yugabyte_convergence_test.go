package cmd

import (
	"bytes"
	"context"
	"errors"
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
// nodes whose tserver-scope precheck reports a change, a restart brings its
// node's process current, and an engine install selects testYugabyteEngine and
// restarts only the master, leaving the tserver on the previous binary.
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

func (o *fakeYugabyteOps) Restart(_ context.Context, node *orchestrator.Task, scope string) error {
	o.log.add("restart-" + scope + ":" + node.Host)
	o.universe.mu.Lock()
	if o.universe.processes == nil {
		o.universe.processes = map[string]yugabyteProcessState{}
	}
	o.universe.processes[node.Host+"/yb-"+scope] = yugabyteProcessCurrent
	delete(o.universe.staleBinaries, node.Host+"/yb-"+scope)
	o.universe.mu.Unlock()
	o.universe.recover(node.Host)
	return nil
}

func (o *fakeYugabyteOps) InstallEngine(_ context.Context, node *orchestrator.Task) error {
	o.log.add("install-engine:" + node.Host)
	u := o.universe
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.engines == nil {
		u.engines = map[string]string{}
	}
	if u.processes == nil {
		u.processes = map[string]yugabyteProcessState{}
	}
	if u.staleBinaries == nil {
		u.staleBinaries = map[string]bool{}
	}
	u.engines[node.Host] = testYugabyteEngine
	u.processes[node.Host+"/yb-tserver"] = yugabyteProcessStale
	u.staleBinaries[node.Host+"/yb-tserver"] = true
	return nil
}

func (o *fakeYugabyteOps) CompleteEngine(_ context.Context, node *orchestrator.Task) error {
	o.log.add("complete-engine:" + node.Host)
	return nil
}

// previousEngineUniverse is a serving universe whose nodes all joined on
// 2025.2.3.0.
func previousEngineUniverse() *fakeUniverse {
	return &fakeUniverse{
		serving: servingAll(),
		engines: map[string]string{"yb-1": "2025.2.3.0", "yb-2": "2025.2.3.0", "yb-3": "2025.2.3.0"},
	}
}

// configConverged marks every master's role converged, so the config passes
// change nothing after the engine step.
func configConverged() map[string]bool {
	return map[string]bool{"yugabyte[master]:yb-1": true, "yugabyte[master]:yb-2": true, "yugabyte[master]:yb-3": true}
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
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, planReleaseHostConvergence(plan, manifest), testYugabyteEngine)
	for _, want := range []string{
		"· Yugabyte, one node at a time: masters of yb-1 -> yb-2 -> yb-3",
		"first, joined nodes not on Yugabyte 2026.1.1.2 upgrade in order (every master, then every tserver, then finalize); each node's installed engine is checked at apply",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plan output lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, planReleaseHostConvergence(plan, manifest), "")
	if !strings.Contains(out.String(), "the pin and each node's engine are checked at apply") {
		t.Fatalf("plan without a pinned engine:\n%s", out.String())
	}
}

// An engine-drifted universe installs the engine on every node with a
// master-only restart, restarts every tserver, completes every node, and
// finalizes before any node's configuration converges.
func TestReleaseYugabyteConvergenceUpgradesTheEngineBeforeConfig(t *testing.T) {
	universe := previousEngineUniverse()
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	converged := configConverged()
	delete(converged, "yugabyte[master]:yb-2")
	fake := &serviceReleaseProvisioner{log: ops.log, converged: converged, afterProvision: func(key string) {
		universe.mu.Lock()
		defer universe.mu.Unlock()
		if len(universe.finalized) != 1 || len(universe.pending) != 0 {
			t.Errorf("%s converged before the engine upgrade was finalized", key)
		}
	}}
	run := runServiceConvergence(t, yugabyteReleaseManifest(), fake, &releaseServiceOps{yugabyte: ops}, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{
		"install-engine:yb-1", "install-engine:yb-2", "install-engine:yb-3",
		"restart-tserver:yb-1", "restart-tserver:yb-2", "restart-tserver:yb-3",
		"complete-engine:yb-1", "complete-engine:yb-2", "complete-engine:yb-3",
		"provision:yugabyte[master]:yb-2",
	}
	if !slices.Equal(run.events, want) {
		t.Fatalf("events = %v\nwant     %v", run.events, want)
	}
	wantMarkers := []string{"yb-1=2026.1.1.2", "yb-2=2026.1.1.2", "yb-3=2026.1.1.2", "yb-1=", "yb-2=", "yb-3="}
	if !slices.Equal(universe.markerWrites, wantMarkers) || len(universe.finalized) != 1 {
		t.Fatalf("markers %v, finalized %v", universe.markerWrites, universe.finalized)
	}
	if !strings.Contains(run.out, "upgrading engine 2025.2.3.0 -> 2026.1.1.2 (masters, tservers, finalize)") {
		t.Fatalf("output:\n%s", run.out)
	}
}

func TestReleaseYugabyteEngineUpgradeStopsWhenTheMastersRefuse(t *testing.T) {
	universe := previousEngineUniverse()
	universe.unsafeCalls = -1
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, configConverged(), false)
	if run.err == nil || !strings.Contains(run.err.Error(), "engine: install on yb-1") || !strings.Contains(run.err.Error(), "would lose its leader") {
		t.Fatalf("err = %v, want the first install refused", run.err)
	}
	if len(run.events) != 0 || len(universe.finalized) != 0 {
		t.Fatalf("changed without the masters' consent: events %v, finalized %v", run.events, universe.finalized)
	}
	if len(universe.pending) != 3 {
		t.Fatalf("markers %v; a stopped upgrade must leave them for the rerun", universe.pending)
	}
}

func TestReleaseYugabyteEngineUpgradeStopsBeforeConfigWhenFinalizeFails(t *testing.T) {
	universe := previousEngineUniverse()
	universe.finalizeErr = errors.New("upgrade_ysql failed")
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, nil, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "not finalized") {
		t.Fatalf("err = %v, want the finalize failure", run.err)
	}
	for _, event := range run.events {
		if strings.HasPrefix(event, "provision:") {
			t.Fatalf("configuration converged after a failed finalize: %v", run.events)
		}
	}
	if len(universe.pending) != 3 {
		t.Fatalf("markers %v; the rerun must still finalize", universe.pending)
	}
}

// A rerun after an interrupted upgrade installs only on the nodes still on
// the previous engine and restarts every tserver still on the previous
// binary, including one on a node whose engine was installed before.
func TestReleaseYugabyteEngineUpgradeResumesAMixedUniverse(t *testing.T) {
	universe := &fakeUniverse{
		serving:       servingAll(),
		engines:       map[string]string{"yb-2": "2025.2.3.0", "yb-3": "2025.2.3.0"},
		pending:       map[string]bool{"yb-1": true, "yb-2": true, "yb-3": true},
		processes:     map[string]yugabyteProcessState{"yb-1/yb-tserver": yugabyteProcessStale},
		staleBinaries: map[string]bool{"yb-1/yb-tserver": true},
	}
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, configConverged(), false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{
		"install-engine:yb-2", "install-engine:yb-3",
		"restart-tserver:yb-1", "restart-tserver:yb-2", "restart-tserver:yb-3",
		"complete-engine:yb-1", "complete-engine:yb-2", "complete-engine:yb-3",
	}
	if !slices.Equal(run.events, want) {
		t.Fatalf("events = %v\nwant     %v", run.events, want)
	}
	if len(universe.finalized) != 1 || len(universe.pending) != 0 {
		t.Fatalf("finalized %v, markers %v", universe.finalized, universe.pending)
	}
}

// Every node already runs the pinned engine, but a finalize marker shows the
// last upgrade stopped before finalizing: the rerun finalizes without
// reinstalling or restarting anything.
func TestReleaseYugabyteEngineUpgradeFinishesAnUnfinalizedUpgrade(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), pending: map[string]bool{"yb-3": true}}
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, configConverged(), false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{"complete-engine:yb-1", "complete-engine:yb-2", "complete-engine:yb-3"}
	if !slices.Equal(run.events, want) || len(universe.finalized) != 1 || len(universe.pending) != 0 {
		t.Fatalf("events %v, finalized %v, markers %v", run.events, universe.finalized, universe.pending)
	}
}

func TestReleaseYugabyteEngineUpgradeDryRunNeitherChangesNorProbes(t *testing.T) {
	universe := previousEngineUniverse()
	ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
	run := runYugabyteReleaseConvergence(t, ops, nil, true)
	if run.err != nil {
		t.Fatalf("dry-run: %v\n%s", run.err, run.out)
	}
	if len(run.events) != 0 || len(universe.markerWrites) != 0 || universe.probes != 0 || len(universe.asked) != 0 || len(universe.finalized) != 0 {
		t.Fatalf("dry-run mutated or probed the masters: events %v, markers %v, probes %d, asked %v", run.events, universe.markerWrites, universe.probes, universe.asked)
	}
	if universe.engineReads != 3 {
		t.Fatalf("engine reads = %d, want one per node", universe.engineReads)
	}
	if !strings.Contains(run.out, "[DRY-RUN] would upgrade engine 2025.2.3.0 -> 2026.1.1.2 (masters, tservers, finalize), installing it on yb-1 -> yb-2 -> yb-3") {
		t.Fatalf("dry-run output:\n%s", run.out)
	}
}

func TestReleaseYugabyteEngineDowngradeIsRefused(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		universe := &fakeUniverse{serving: servingAll(), engines: map[string]string{"yb-2": "2026.2.0.0"}}
		ops := &fakeYugabyteOps{log: &serviceEventLog{}, universe: universe}
		run := runYugabyteReleaseConvergence(t, ops, nil, dryRun)
		if run.err == nil || !strings.Contains(run.err.Error(), "refusing to downgrade the Yugabyte engine on yb-2 from 2026.2.0.0 to the release's 2026.1.1.2") {
			t.Fatalf("dry-run %v: err = %v, want the downgrade refused", dryRun, run.err)
		}
		if len(run.events) != 0 || len(universe.markerWrites) != 0 || len(universe.asked) != 0 {
			t.Fatalf("dry-run %v: a refused downgrade changed the universe: events %v, markers %v", dryRun, run.events, universe.markerWrites)
		}
	}
}
