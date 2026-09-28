package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	"github.com/spf13/cobra"
)

type nodeBaselineRun struct {
	mu     sync.Mutex
	events []string
}

func (r *nodeBaselineRun) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func nodeBaselineConvergence(t *testing.T, manifest *inventory.Manifest, run *nodeBaselineRun, verifyErr error) (*releaseHostConvergence, []releaseHostConvergenceStep, *bytes.Buffer) {
	t.Helper()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var steps []releaseHostConvergenceStep
	for _, step := range planReleaseHostConvergence(plan, manifest) {
		if step.Kind == releaseHostStepNodeBaseline {
			steps = append(steps, step)
		}
	}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	return &releaseHostConvergence{
		cmd:      cmd,
		manifest: manifest,
		nodeHostFn: func(_ context.Context, host inventory.Host, dryRun bool, _ io.Writer) error {
			event := "converge:" + host.Name
			if dryRun {
				event = "dry-run:" + host.Name
			}
			run.add(event)
			return nil
		},
		verifySSHFn: func(_ context.Context, hosts []string) error {
			sorted := slices.Clone(hosts)
			slices.Sort(sorted)
			run.add("ssh-login:" + strings.Join(sorted, ","))
			return verifyErr
		},
	}, steps, &out
}

// The host convergence stage runs, and `cluster release plan` prints, the node
// baseline first, then Privateer, Redis, the data services, the Kafka topics
// and MirrorMaker2 last.
func TestReleaseHostConvergenceStageOrder(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	manifest.Infrastructure.Redis = &inventory.RedisConfig{Enabled: true, Instances: []inventory.RedisInstance{{Name: "platform", Host: "central-eu-1", Port: 6379}}}
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	steps := planReleaseHostConvergence(plan, manifest)
	var kinds []string
	for _, step := range steps {
		if len(kinds) == 0 || kinds[len(kinds)-1] != step.Kind {
			kinds = append(kinds, step.Kind)
		}
	}
	wantKinds := []string{releaseHostStepNodeBaseline, releaseHostStepPrivateer, releaseHostStepRedis, releaseHostStepService, releaseHostStepKafkaTopics, releaseHostStepMirrorMaker}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("stage order = %v, want %v", kinds, wantKinds)
	}

	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", manifest, steps, "")
	last := -1
	for _, line := range []string{
		"· Node baseline and OS tuning",
		"· Privateer binary, seed peers, and seed DNS",
		"· Redis platform",
		"· Kafka ",
		"· Kafka topics created when missing",
		"· MirrorMaker2 workers",
	} {
		at := strings.Index(out.String(), line)
		if at <= last {
			t.Fatalf("plan line %q at %d, want after %d:\n%s", line, at, last, out.String())
		}
		last = at
	}
}

// Every planned host converges its node baseline, the canary alone first, and
// a new SSH login to each host of a wave gates the next wave.
func TestReleaseNodeBaselineConvergesInWavesGatedBySSHLogin(t *testing.T) {
	run := &nodeBaselineRun{}
	convergence, steps, _ := nodeBaselineConvergence(t, multiRegionReleaseManifest(), run, nil)
	if err := convergence.run(context.Background(), steps, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(run.events) != 5 {
		t.Fatalf("events = %v, want canary, gate, two batch hosts, gate", run.events)
	}
	if got := run.events[:2]; !slices.Equal(got, []string{"converge:regional-eu-1", "ssh-login:regional-eu-1"}) {
		t.Fatalf("canary events = %v", got)
	}
	batch := slices.Clone(run.events[2:4])
	slices.Sort(batch)
	if !slices.Equal(batch, []string{"converge:central-eu-1", "converge:regional-us-1"}) {
		t.Fatalf("batch events = %v", run.events[2:4])
	}
	if run.events[4] != "ssh-login:central-eu-1,regional-us-1" {
		t.Fatalf("batch gate = %q", run.events[4])
	}
}

// A host that refuses a new SSH login after the canary converged stops the
// stage: no later host is touched.
func TestReleaseNodeBaselineStopsWhenSSHLoginFails(t *testing.T) {
	run := &nodeBaselineRun{}
	convergence, steps, _ := nodeBaselineConvergence(t, multiRegionReleaseManifest(), run, errors.New("permission denied (publickey)"))
	err := convergence.run(context.Background(), steps, false)
	if err == nil || !strings.Contains(err.Error(), "node baseline") || !strings.Contains(err.Error(), "publickey") {
		t.Fatalf("err = %v, want the SSH login failure", err)
	}
	if !slices.Equal(run.events, []string{"converge:regional-eu-1", "ssh-login:regional-eu-1"}) {
		t.Fatalf("events = %v, want only the canary", run.events)
	}
}

// A dry run previews every host and opens no login.
func TestReleaseNodeBaselineDryRunPreviewsWithoutGate(t *testing.T) {
	run := &nodeBaselineRun{}
	convergence, steps, _ := nodeBaselineConvergence(t, multiRegionReleaseManifest(), run, nil)
	if err := convergence.run(context.Background(), steps, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	slices.Sort(run.events)
	if !slices.Equal(run.events, []string{"dry-run:central-eu-1", "dry-run:regional-eu-1", "dry-run:regional-us-1"}) {
		t.Fatalf("events = %v", run.events)
	}
}

// convergeNodeHost applies only the roles whose check-mode precheck reports a
// change, renders the host's tuning profile, and applies nothing in a dry run.
func TestConvergeNodeHostAppliesOnlyChangedRoles(t *testing.T) {
	baseline := &inspectingProvisioner{}
	tuning := &inspectingProvisioner{inspection: provisioner.ChangeInspection{Changed: true, Tasks: []string{"Node_tuning | write core sysctl baseline"}}}
	original := nodeHostProvisioners
	nodeHostProvisioners = func(*ssh.Pool) (provisioner.Provisioner, provisioner.Provisioner, error) {
		return baseline, tuning, nil
	}
	t.Cleanup(func() { nodeHostProvisioners = original })

	manifest := multiRegionReleaseManifest()
	host := manifest.Hosts["regional-eu-1"]

	var out bytes.Buffer
	if err := convergeNodeHost(context.Background(), &out, nil, manifest, host, false, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if slices.Contains(baseline.calls, "provision") || slices.Contains(tuning.calls, "provision") {
		t.Fatalf("dry-run provisioned: baseline=%v tuning=%v", baseline.calls, tuning.calls)
	}
	for _, want := range []string{"node baseline already converged", "[DRY-RUN] node tuning would converge", "Node_tuning | write core sysctl baseline"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out.String())
		}
	}

	out.Reset()
	if err := convergeNodeHost(context.Background(), &out, nil, manifest, host, false, false); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if slices.Contains(baseline.calls, "provision") {
		t.Fatalf("unchanged node baseline was provisioned: %v", baseline.calls)
	}
	if !slices.Contains(tuning.calls, "provision") {
		t.Fatalf("changed node tuning was not provisioned: %v", tuning.calls)
	}

	edge := manifest.Hosts["edge-eu-1"]
	roles, err := nodeHostRoles(nil, manifest, edge, false)
	if err != nil {
		t.Fatalf("roles: %v", err)
	}
	if got := roles[1].config.Metadata["profile"]; got != "edge" {
		t.Fatalf("edge host tuning profile = %v, want edge", got)
	}
}
