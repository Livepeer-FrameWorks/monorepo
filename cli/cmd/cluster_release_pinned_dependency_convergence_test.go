package cmd

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

// pinnedDependencyManifest runs a signer on sig-1 and the EU gateway on gw-1
// and gw-2.
func pinnedDependencyManifest() *inventory.Manifest {
	hosts := map[string]inventory.Host{}
	for _, name := range []string{"sig-1", "gw-1", "gw-2"} {
		hosts[name] = serviceHost(name)
	}
	return &inventory.Manifest{
		Profile: "dev",
		Hosts:   hosts,
		Services: map[string]inventory.ServiceConfig{
			"livepeer-gateway-eu": {Enabled: true, Mode: "native", Deploy: "livepeer-gateway", Hosts: []string{"gw-2", "gw-1"}},
			"livepeer-signer":     {Enabled: true, Mode: "native", Host: "sig-1"},
		},
	}
}

func runPinnedDependencyConvergence(t *testing.T, manifest *inventory.Manifest, fake *serviceReleaseProvisioner, dryRun bool) serviceConvergenceRun {
	t.Helper()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	original := taskProvisioner
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { taskProvisioner = original })
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	c := &releaseHostConvergence{
		cmd:         cmd,
		manifest:    manifest,
		runtimeData: gatewayRuntimeData(),
		sharedEnv:   map[string]string{"ARBITRUM_RPC_ENDPOINT": "https://arb.example.invalid"},
		nodeHostFn:  func(context.Context, inventory.Host, bool, io.Writer) error { return nil },
		verifySSHFn: func(context.Context, []string) error { return nil },
		serviceOps:  &releaseServiceOps{timing: &serviceGateTiming{Timeout: 50 * time.Millisecond, Interval: time.Millisecond}},
	}
	err = c.runReleasePinnedDependencies(context.Background(), planReleasePinnedDependencies(plan), dryRun)
	return serviceConvergenceRun{events: fake.log.snapshot(), out: out.String(), err: err}
}

// A release that moves the go-livepeer pin converges the signer, then each
// gateway host alone; a host whose role reports no change is skipped.
func TestReleasePinnedDependenciesConvergeOneHostAtATime(t *testing.T) {
	fake := &serviceReleaseProvisioner{log: &serviceEventLog{}, converged: map[string]bool{"livepeer-gateway:gw-2": true}}
	run := runPinnedDependencyConvergence(t, pinnedDependencyManifest(), fake, false)
	if run.err != nil {
		t.Fatalf("run: %v\n%s", run.err, run.out)
	}
	want := []string{"livepeer-signer:sig-1", "livepeer-gateway:gw-1"}
	if got := dependencyEvents(run.events); !slices.Equal(got, want) {
		t.Fatalf("converged %v, want %v", got, want)
	}
}

func TestReleasePinnedDependenciesStopAtTheFirstFailedHost(t *testing.T) {
	fake := &serviceReleaseProvisioner{log: &serviceEventLog{}, failing: map[string]bool{"livepeer-gateway:gw-1": true}}
	run := runPinnedDependencyConvergence(t, pinnedDependencyManifest(), fake, false)
	if run.err == nil || !strings.Contains(run.err.Error(), "livepeer-gateway on gw-1") {
		t.Fatalf("err = %v, want the gw-1 failure", run.err)
	}
	if slices.Contains(dependencyEvents(run.events), "livepeer-gateway:gw-2") {
		t.Fatalf("gw-2 changed after gw-1 failed: %v", run.events)
	}
}

func TestReleasePinnedDependenciesDryRunChangesNothing(t *testing.T) {
	fake := &serviceReleaseProvisioner{log: &serviceEventLog{}}
	run := runPinnedDependencyConvergence(t, pinnedDependencyManifest(), fake, true)
	if run.err != nil || len(dependencyEvents(run.events)) != 0 || strings.Count(run.out, "[DRY-RUN] would converge") != 3 {
		t.Fatalf("dry-run: err %v, events %v, output:\n%s", run.err, run.events, run.out)
	}
}

func TestReleasePinnedDependenciesPlanLine(t *testing.T) {
	manifest := pinnedDependencyManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var out bytes.Buffer
	describeReleasePinnedDependencies(&out, planReleasePinnedDependencies(plan))
	if !strings.Contains(out.String(), "one host at a time: livepeer-signer@sig-1 -> livepeer-gateway@gw-1 -> livepeer-gateway@gw-2") {
		t.Fatalf("plan line:\n%s", out.String())
	}
	if got := planReleaseHostConvergence(plan, manifest); slices.ContainsFunc(got, func(step releaseHostConvergenceStep) bool {
		return step.Task != nil && strings.HasPrefix(step.Task.Type, "livepeer-")
	}) {
		t.Fatal("go-livepeer hosts converge before the platform services they call")
	}
}
