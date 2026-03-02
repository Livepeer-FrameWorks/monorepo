package orchestrator

import (
	"context"
	"sort"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestEffectivePrivateerHosts_FiltersEdge(t *testing.T) {
	hosts := map[string]inventory.Host{
		"core1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
		"core2": {ExternalIP: "10.0.0.2", Roles: []string{"data"}},
		"edge1": {ExternalIP: "10.0.0.3", Roles: []string{"edge"}},
	}
	svc := inventory.ServiceConfig{Enabled: true}

	got := EffectivePrivateerHosts(svc, hosts)
	sort.Strings(got)

	if len(got) != 2 {
		t.Fatalf("expected 2 hosts, got %d: %v", len(got), got)
	}
	if got[0] != "core1" || got[1] != "core2" {
		t.Fatalf("expected [core1 core2], got %v", got)
	}
}

func TestEffectivePrivateerHosts_ExplicitHosts(t *testing.T) {
	hosts := map[string]inventory.Host{
		"core1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
		"edge1": {ExternalIP: "10.0.0.2", Roles: []string{"edge"}},
	}
	svc := inventory.ServiceConfig{
		Enabled: true,
		Hosts:   []string{"edge1"},
	}

	got := EffectivePrivateerHosts(svc, hosts)
	if len(got) != 1 || got[0] != "edge1" {
		t.Fatalf("expected [edge1], got %v", got)
	}
}

func TestEffectivePrivateerHosts_SingleHostField(t *testing.T) {
	hosts := map[string]inventory.Host{
		"core1": {ExternalIP: "10.0.0.1"},
		"core2": {ExternalIP: "10.0.0.2"},
	}
	svc := inventory.ServiceConfig{
		Enabled: true,
		Host:    "core1",
	}

	got := EffectivePrivateerHosts(svc, hosts)
	if len(got) != 1 || got[0] != "core1" {
		t.Fatalf("expected [core1], got %v", got)
	}
}

func TestEffectivePrivateerHosts_AllCoreWhenNoEdge(t *testing.T) {
	hosts := map[string]inventory.Host{
		"core1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
		"core2": {ExternalIP: "10.0.0.2", Roles: []string{"data"}},
	}
	svc := inventory.ServiceConfig{Enabled: true}

	got := EffectivePrivateerHosts(svc, hosts)
	sort.Strings(got)

	if len(got) != 2 {
		t.Fatalf("expected 2 hosts, got %d: %v", len(got), got)
	}
}

func TestPlan_PrivateerDepsExcludeEdge(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
			"core2": {ExternalIP: "10.0.0.2", Roles: []string{"data"}},
			"edge1": {ExternalIP: "10.0.0.3", Roles: []string{"edge"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core1"},
			"privateer":     {Enabled: true},
			"bridge":        {Enabled: true, Host: "core1"},
		},
	}

	planner := NewPlanner(manifest)
	plan, err := planner.Plan(context.Background(), ProvisionOptions{Phase: PhaseAll})
	if err != nil {
		t.Fatalf("Plan() failed: %v", err)
	}

	// Collect all task names
	taskNames := map[string]bool{}
	for _, task := range plan.AllTasks {
		taskNames[task.Name] = true
	}

	// Privateer tasks should only exist for core hosts
	if taskNames["privateer@edge1"] {
		t.Error("privateer@edge1 task should not exist")
	}
	if !taskNames["privateer@core1"] {
		t.Error("privateer@core1 task should exist")
	}
	if !taskNames["privateer@core2"] {
		t.Error("privateer@core2 task should exist")
	}

	// Bridge should depend on privateer@core1 and privateer@core2, NOT privateer@edge1
	for _, task := range plan.AllTasks {
		if task.Name == "bridge" {
			for _, dep := range task.DependsOn {
				if dep == "privateer@edge1" {
					t.Error("bridge depends on privateer@edge1 which should not exist")
				}
			}
		}
	}
}

func TestPlan_InterfaceDepsExcludeEdge(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core1": {ExternalIP: "10.0.0.1", Roles: []string{"control"}},
			"edge1": {ExternalIP: "10.0.0.2", Roles: []string{"edge"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core1"},
			"privateer":     {Enabled: true},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"caddy": {Enabled: true, Host: "core1"},
		},
	}

	planner := NewPlanner(manifest)
	plan, err := planner.Plan(context.Background(), ProvisionOptions{Phase: PhaseAll})
	if err != nil {
		t.Fatalf("Plan() failed: %v", err)
	}

	for _, task := range plan.AllTasks {
		if task.Name == "caddy" {
			for _, dep := range task.DependsOn {
				if dep == "privateer@edge1" {
					t.Error("caddy depends on privateer@edge1 which should not exist")
				}
			}
		}
	}
}
