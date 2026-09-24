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

// deferringMirrorMaker is a MirrorMaker2 provisioner whose role always reports
// drift. It records, per host, whether provisioning asked the role to defer its
// restart, and can fail one host.
type deferringMirrorMaker struct {
	fakeTaskProvisioner
	mu       *sync.Mutex
	events   *[]string
	failHost string
}

func (d *deferringMirrorMaker) Detect(context.Context, inventory.Host) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: true, Running: true}, nil
}

func (d *deferringMirrorMaker) WouldChange(context.Context, inventory.Host, provisioner.ServiceConfig, []string) (bool, error) {
	return true, nil
}

func (d *deferringMirrorMaker) Provision(_ context.Context, host inventory.Host, config provisioner.ServiceConfig) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	deferred := config.Metadata[provisioner.KafkaMirrorMakerDeferRestartKey] == true
	*d.events = append(*d.events, "provision:"+host.Name+":defer="+map[bool]string{true: "yes", false: "no"}[deferred])
	if host.Name == d.failHost {
		return errors.New("role failed")
	}
	return nil
}

func mirrorMakerConvergenceSteps(t *testing.T, manifest *inventory.Manifest) []releaseHostConvergenceStep {
	t.Helper()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var steps []releaseHostConvergenceStep
	for _, step := range planReleaseHostConvergence(plan, manifest) {
		if step.Kind == releaseHostStepMirrorMaker {
			steps = append(steps, step)
		}
	}
	if len(steps) != 2 {
		t.Fatalf("mirror maker steps = %d, want 2", len(steps))
	}
	return steps
}

func runMirrorMakerConvergence(t *testing.T, failHost string) ([]string, string, error) {
	t.Helper()
	manifest := multiRegionReleaseManifest()
	steps := mirrorMakerConvergenceSteps(t, manifest)

	var mu sync.Mutex
	var events []string
	fake := &deferringMirrorMaker{mu: &mu, events: &events, failHost: failHost}
	original := taskProvisioner
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { taskProvisioner = original })

	// Both workers restart only once each has seen the other start, so a
	// sequential restart deadlocks into the timeout.
	var started sync.WaitGroup
	started.Add(2)
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	c := &releaseHostConvergence{
		cmd:         cmd,
		manifest:    manifest,
		runtimeData: map[string]any{},
		sharedEnv:   map[string]string{},
		mirrorMakerRestartPendingFn: func(_ context.Context, task *orchestrator.Task) (bool, error) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "probe:"+task.Host)
			return true, nil
		},
		mirrorMakerRestartFn: func(_ context.Context, task *orchestrator.Task) error {
			started.Done()
			done := make(chan struct{})
			go func() { started.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				return errors.New("restart was not concurrent with the other worker's")
			}
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "restart:"+task.Host)
			return nil
		},
	}
	err := c.run(context.Background(), steps, false)
	return events, out.String(), err
}

func TestReleaseHostConvergenceRestartsMirrorMakersTogetherAfterConfig(t *testing.T) {
	events, out, err := runMirrorMakerConvergence(t, "")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	provisions := []string{"provision:regional-eu-1:defer=yes", "provision:regional-us-1:defer=yes"}
	if !slices.Equal(events[:2], provisions) {
		t.Fatalf("events = %v, want both workers converged with a deferred restart first", events)
	}
	if !slices.Equal(events[2:4], []string{"probe:regional-eu-1", "probe:regional-us-1"}) {
		t.Fatalf("events = %v, want the pending probes after every worker converged", events)
	}
	restarts := slices.Clone(events[4:])
	slices.Sort(restarts)
	if !slices.Equal(restarts, []string{"restart:regional-eu-1", "restart:regional-us-1"}) {
		t.Fatalf("events = %v, want both workers restarted", events)
	}
	if !strings.Contains(out, "Restarting MirrorMaker2 workers together: regional-eu-1, regional-us-1") {
		t.Fatalf("output does not announce the joint restart:\n%s", out)
	}
}

func TestReleaseHostConvergenceRestartsConvergedMirrorMakersWhenALaterOneFails(t *testing.T) {
	events, out, err := runMirrorMakerConvergence(t, "regional-us-1")
	if err == nil || !strings.Contains(err.Error(), "kafka-mirrormaker on regional-us-1") {
		t.Fatalf("err = %v, want the regional-us-1 failure\n%s", err, out)
	}
	var restarts []string
	for _, event := range events {
		if strings.HasPrefix(event, "restart:") {
			restarts = append(restarts, event)
		}
	}
	slices.Sort(restarts)
	if !slices.Equal(restarts, []string{"restart:regional-eu-1", "restart:regional-us-1"}) {
		t.Fatalf("events = %v, want pending workers restarted before the failure returns", events)
	}
}

func TestReleaseHostConvergenceSkipsMirrorMakerRestartWhenNothingPending(t *testing.T) {
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	c := &releaseHostConvergence{
		cmd:                         cmd,
		mirrorMakerRestartPendingFn: func(context.Context, *orchestrator.Task) (bool, error) { return false, nil },
		mirrorMakerRestartFn: func(context.Context, *orchestrator.Task) error {
			t.Fatal("restarted a worker without a pending restart")
			return nil
		},
	}
	if err := c.restartPendingMirrorMakers(context.Background(), []*orchestrator.Task{{Host: "regional-eu-1"}}, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no restart pending") {
		t.Fatalf("output = %q", out.String())
	}
}
