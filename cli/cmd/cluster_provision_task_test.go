package cmd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

// fakeTaskProvisioner records the lifecycle calls provisionTask makes, in order.
type fakeTaskProvisioner struct {
	state       *detect.ServiceState
	wouldChange bool
	calls       []string
}

func (f *fakeTaskProvisioner) Detect(context.Context, inventory.Host) (*detect.ServiceState, error) {
	f.calls = append(f.calls, "detect")
	return f.state, nil
}

func (f *fakeTaskProvisioner) WouldChange(_ context.Context, _ inventory.Host, _ provisioner.ServiceConfig, tags []string) (bool, error) {
	if len(tags) > 0 {
		f.calls = append(f.calls, "precheck:"+tags[0])
		return false, nil
	}
	f.calls = append(f.calls, "precheck")
	return f.wouldChange, nil
}

func (f *fakeTaskProvisioner) Provision(context.Context, inventory.Host, provisioner.ServiceConfig) error {
	f.calls = append(f.calls, "provision")
	f.state = &detect.ServiceState{Exists: true, Running: true}
	return nil
}

func (f *fakeTaskProvisioner) Deploy(context.Context, inventory.Host, provisioner.ServiceConfig) error {
	f.calls = append(f.calls, "deploy")
	return nil
}

func (f *fakeTaskProvisioner) Validate(context.Context, inventory.Host, provisioner.ServiceConfig) error {
	f.calls = append(f.calls, "validate")
	return nil
}

func (f *fakeTaskProvisioner) Initialize(context.Context, inventory.Host, provisioner.ServiceConfig) error {
	f.calls = append(f.calls, "initialize")
	return nil
}

func (f *fakeTaskProvisioner) Cleanup(context.Context, inventory.Host, provisioner.ServiceConfig) error {
	return nil
}

func (f *fakeTaskProvisioner) GetName() string { return "redis" }

func runFakeProvisionTask(t *testing.T, fake *fakeTaskProvisioner, beforeChange func() error) (*taskProvisionOutcome, error) {
	t.Helper()
	original := taskProvisioner
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { taskProvisioner = original })
	manifest := &inventory.Manifest{Infrastructure: inventory.InfrastructureConfig{Redis: &inventory.RedisConfig{
		Enabled:   true,
		Mode:      "docker",
		Instances: []inventory.RedisInstance{{Name: "foghorn", Host: "media-1", Port: 6380}},
	}}}
	task := &orchestrator.Task{Name: "redis-foghorn", Type: "redis", ServiceID: "redis", InstanceID: "foghorn", Host: "media-1", Phase: orchestrator.PhaseInfrastructure}
	return provisionTask(context.Background(), task, inventory.Host{Name: "media-1"}, nil, manifest, false, false, map[string]any{}, "", nil, nil, nil, beforeChange)
}

func TestProvisionTaskGatesOnlyAppliedChanges(t *testing.T) {
	tests := []struct {
		name        string
		state       *detect.ServiceState
		wouldChange bool
		wantCalls   []string
	}{
		{
			name:        "running service that already matches skips the gate",
			state:       &detect.ServiceState{Exists: true, Running: true},
			wouldChange: false,
			wantCalls:   []string{"detect", "precheck", "validate", "precheck:init", "detect"},
		},
		{
			name:        "running service with drift gates before provisioning",
			state:       &detect.ServiceState{Exists: true, Running: true},
			wouldChange: true,
			wantCalls:   []string{"detect", "precheck", "gate", "provision", "validate", "precheck:init", "detect"},
		},
		{
			name:      "stopped service gates before restoring it",
			state:     &detect.ServiceState{Exists: true, Running: false},
			wantCalls: []string{"detect", "gate", "provision", "validate", "precheck:init", "detect"},
		},
		{
			name:      "first install gates before provisioning",
			state:     &detect.ServiceState{},
			wantCalls: []string{"detect", "gate", "provision", "validate", "precheck:init", "detect"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeTaskProvisioner{state: tt.state, wouldChange: tt.wouldChange}
			if _, err := runFakeProvisionTask(t, fake, func() error {
				fake.calls = append(fake.calls, "gate")
				return nil
			}); err != nil {
				t.Fatalf("provisionTask: %v", err)
			}
			if !reflect.DeepEqual(fake.calls, tt.wantCalls) {
				t.Fatalf("calls = %v, want %v", fake.calls, tt.wantCalls)
			}
		})
	}
}

func TestProvisionTaskGateFailureStopsBeforeProvision(t *testing.T) {
	fake := &fakeTaskProvisioner{state: &detect.ServiceState{Exists: true, Running: true}, wouldChange: true}
	gateErr := errors.New("yugabyte universe is not safe to change")
	_, err := runFakeProvisionTask(t, fake, func() error { return gateErr })
	if !errors.Is(err, gateErr) {
		t.Fatalf("provisionTask error = %v, want the gate error", err)
	}
	if want := []string{"detect", "precheck"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
}
