package cmd

import (
	"bytes"
	"context"
	"slices"
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

// serviceEventLog records the provisions and probes of a service convergence
// run in order.
type serviceEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *serviceEventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *serviceEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// serviceTaskKey names a rendered task: its deploy name, the restart scope a
// Yugabyte pass renders with, and its host.
func serviceTaskKey(host inventory.Host, config provisioner.ServiceConfig) string {
	key := config.DeployName
	if scope, ok := config.Metadata["restart_scope"].(string); ok && scope != "" {
		key += "[" + scope + "]"
	}
	return key + ":" + host.Name
}

// serviceReleaseProvisioner is a role whose precheck reports drift on every
// task not in converged, which records each apply and then runs
// afterProvision.
type serviceReleaseProvisioner struct {
	fakeTaskProvisioner
	log            *serviceEventLog
	converged      map[string]bool
	afterProvision func(key string)
}

func (p *serviceReleaseProvisioner) Detect(context.Context, inventory.Host) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: true, Running: true}, nil
}

func (p *serviceReleaseProvisioner) WouldChange(_ context.Context, host inventory.Host, config provisioner.ServiceConfig, tags []string) (bool, error) {
	if len(tags) > 0 {
		return false, nil
	}
	return !p.converged[serviceTaskKey(host, config)], nil
}

func (p *serviceReleaseProvisioner) Provision(_ context.Context, host inventory.Host, config provisioner.ServiceConfig) error {
	key := serviceTaskKey(host, config)
	p.log.add("provision:" + key)
	if p.afterProvision != nil {
		p.afterProvision(key)
	}
	return nil
}

func (p *serviceReleaseProvisioner) Validate(context.Context, inventory.Host, provisioner.ServiceConfig) error {
	return nil
}

type serviceConvergenceRun struct {
	events []string
	out    string
	err    error
}

// runServiceConvergence plans and runs the host convergence stage of manifest
// with fake as every task's role.
func runServiceConvergence(t *testing.T, manifest *inventory.Manifest, fake *serviceReleaseProvisioner, ops *releaseServiceOps, dryRun bool) serviceConvergenceRun {
	t.Helper()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	steps := planReleaseHostConvergence(plan, manifest)
	original := taskProvisioner
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { taskProvisioner = original })

	if ops.timing == nil {
		ops.timing = &serviceGateTiming{Timeout: 50 * time.Millisecond, Interval: time.Millisecond}
	}
	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	c := &releaseHostConvergence{
		cmd:         cmd,
		manifest:    manifest,
		runtimeData: map[string]any{},
		sharedEnv:   map[string]string{},
		verifyMeshFn: func(context.Context, []string) error {
			return nil
		},
		serviceOps: ops,
	}
	err = c.run(context.Background(), steps, dryRun)
	return serviceConvergenceRun{events: fake.log.snapshot(), out: out.String(), err: err}
}

func serviceHost(name string) inventory.Host {
	return inventory.Host{Name: name, ExternalIP: "10.0.0.1", WireguardIP: "10.0.0.1"}
}

func TestWithReleaseRuntimeOverridesLeavesTheTaskUnchanged(t *testing.T) {
	task := &orchestrator.Task{Name: "yugabyte-node-1", Type: "yugabyte", Metadata: map[string]any{"a": 1}}
	scoped := withReleaseRuntimeOverrides(task, map[string]any{"restart_scope": "master"})
	if got := releaseRuntimeOverrides(scoped)["restart_scope"]; got != "master" {
		t.Fatalf("scoped overrides = %v", releaseRuntimeOverrides(scoped))
	}
	if len(releaseRuntimeOverrides(task)) != 0 || len(task.Metadata) != 1 {
		t.Fatalf("original task changed: %v", task.Metadata)
	}
}
