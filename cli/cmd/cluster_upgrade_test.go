package cmd

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestWaitForHealthRetriesUntilSuccess(t *testing.T) {
	var attempts int32
	check := func() error {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			return errors.New("not ready")
		}
		return nil
	}

	if err := waitForHealth(context.Background(), check, 5*time.Millisecond, 50*time.Millisecond); err != nil {
		t.Fatalf("expected health check to succeed, got error: %v", err)
	}
}

func TestWaitForHealthTimeout(t *testing.T) {
	errSentinel := errors.New("still failing")
	check := func() error {
		return errSentinel
	}

	err := waitForHealth(context.Background(), check, 5*time.Millisecond, 30*time.Millisecond)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, errSentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
}

func TestCollectUpgradeableServices_DeduplicatesMultiHost(t *testing.T) {
	plan := &orchestrator.ExecutionPlan{
		Batches: [][]*orchestrator.Task{
			{
				{Name: "privateer-mesh-host-a", ServiceID: "privateer", InstanceID: "host-a", Phase: orchestrator.PhaseMesh},
				{Name: "privateer-mesh-host-b", ServiceID: "privateer", InstanceID: "host-b", Phase: orchestrator.PhaseMesh},
			},
			{
				{Name: "postgres", ServiceID: "postgres", Phase: orchestrator.PhaseInfrastructure},
				{Name: "kafka", ServiceID: "kafka", Phase: orchestrator.PhaseInfrastructure},
			},
			{
				{Name: "bridge@host-a", ServiceID: "bridge", InstanceID: "host-a", Phase: orchestrator.PhaseApplications},
				{Name: "bridge@host-b", ServiceID: "bridge", InstanceID: "host-b", Phase: orchestrator.PhaseApplications},
				{Name: "commodore", ServiceID: "commodore", Phase: orchestrator.PhaseApplications},
			},
		},
	}

	got := collectUpgradeableServices(plan)
	want := []string{"bridge", "commodore"}

	if len(got) != len(want) {
		t.Fatalf("expected %d services, got %d: %v", len(want), len(got), got)
	}
	for i, s := range want {
		if got[i] != s {
			t.Errorf("service[%d]: expected %q, got %q", i, s, got[i])
		}
	}
}

func TestCollectUpgradeableServices_SkipsPrivateerMeshRole(t *testing.T) {
	plan := &orchestrator.ExecutionPlan{
		Batches: [][]*orchestrator.Task{
			{
				{Name: "privateer", ServiceID: "privateer", Phase: orchestrator.PhaseApplications},
				{Name: "bridge", ServiceID: "bridge", Phase: orchestrator.PhaseApplications},
			},
		},
	}

	got := collectUpgradeableServices(plan)
	if len(got) != 1 {
		t.Fatalf("expected 1 service, got %d: %v", len(got), got)
	}
	if got[0] != "bridge" {
		t.Fatalf("expected [bridge], got %v", got)
	}
}

// TestResolveUpgradeHosts_MultiReplicaAppReturnsAllHosts pins that `cluster upgrade` (and `--all`) resolves EVERY
// replica of an HA service, not just the first host — otherwise an upgrade would leave stale replicas behind. A
// two-Foghorn / two-Chandler cluster must resolve to every replica, in manifest order, while single-primary
// infrastructure still resolves to exactly one host.
func TestResolveUpgradeHosts_MultiReplicaAppReturnsAllHosts(t *testing.T) {
	t.Parallel()

	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"edge-a": {Name: "edge-a", ExternalIP: "10.0.0.1", Cluster: "media-us"},
			"edge-b": {Name: "edge-b", ExternalIP: "10.0.0.2", Cluster: "media-us"},
			"db-a":   {Name: "db-a", ExternalIP: "10.0.0.9", Cluster: "core"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn":  {Enabled: true, Hosts: []string{"edge-a", "edge-b"}},
			"chandler": {Enabled: true, Hosts: []string{"edge-a", "edge-b"}},
			"disabled": {Enabled: false, Hosts: []string{"edge-a"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Host: "db-a"},
		},
	}

	for _, svc := range []string{"foghorn", "chandler"} {
		hosts, found := resolveUpgradeHosts(manifest, svc)
		if !found {
			t.Fatalf("%s: expected to resolve hosts", svc)
		}
		got := []string{}
		for _, h := range hosts {
			got = append(got, h.Name)
		}
		if len(got) != 2 || got[0] != "edge-a" || got[1] != "edge-b" {
			t.Fatalf("%s: expected [edge-a edge-b] in order, got %v", svc, got)
		}
	}

	// Single-primary infrastructure resolves to exactly one host.
	pgHosts, pgFound := resolveUpgradeHosts(manifest, "postgres")
	if !pgFound || len(pgHosts) != 1 || pgHosts[0].Name != "db-a" {
		t.Fatalf("postgres: expected single host [db-a], got found=%v hosts=%v", pgFound, pgHosts)
	}

	// A disabled service resolves to nothing.
	if _, found := resolveUpgradeHosts(manifest, "disabled"); found {
		t.Fatalf("disabled: expected no hosts")
	}

	// An unknown service resolves to nothing.
	if _, found := resolveUpgradeHosts(manifest, "does-not-exist"); found {
		t.Fatalf("unknown: expected no hosts")
	}
}

func TestUpgradeRollbackSupported(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version string
		mode    string
		want    bool
	}{
		{name: "native", version: "v0.2.69", mode: "native", want: true},
		{name: "docker", version: "v0.2.69", mode: "docker", want: true},
		{name: "missing version", version: "", mode: "native", want: false},
		{name: "unknown mode", version: "v0.2.69", mode: "unknown", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := upgradeRollbackSupported(tt.version, tt.mode); got != tt.want {
				t.Fatalf("upgradeRollbackSupported(%q,%q)=%v, want %v", tt.version, tt.mode, got, tt.want)
			}
		})
	}
}
