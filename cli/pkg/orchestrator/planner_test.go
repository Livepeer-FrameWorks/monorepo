package orchestrator

import (
	"context"
	"slices"
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

	// Privateer-mesh tasks should only exist for core hosts
	if taskNames["privateer-mesh-edge1"] {
		t.Error("privateer-mesh-edge1 task should not exist")
	}
	if !taskNames["privateer-mesh-core1"] {
		t.Error("privateer-mesh-core1 task should exist")
	}
	if !taskNames["privateer-mesh-core2"] {
		t.Error("privateer-mesh-core2 task should exist")
	}

	// Bridge should depend on privateer-mesh tasks on core hosts only.
	for _, task := range plan.AllTasks {
		if task.Name == "bridge" {
			if slices.Contains(task.DependsOn, "privateer-mesh-edge1") {
				t.Error("bridge depends on privateer-mesh-edge1 which should not exist")
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

func TestPlan_ClickHouseDependsOnSameHostYugabyte(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"yuga-eu-1": {ExternalIP: "10.0.0.1"},
			"yuga-eu-2": {ExternalIP: "10.0.0.2"},
			"yuga-eu-3": {ExternalIP: "10.0.0.3"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Mode:    "native",
				Version: "2025.1.3.2",
				Nodes: []inventory.PostgresNode{
					{Host: "yuga-eu-1", ID: 1},
					{Host: "yuga-eu-2", ID: 2},
					{Host: "yuga-eu-3", ID: 3},
				},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Mode:    "native",
				Version: "25.9.2.1",
				Host:    "yuga-eu-1",
			},
		},
	}

	planner := NewPlanner(manifest)
	plan, err := planner.Plan(context.Background(), ProvisionOptions{Phase: PhaseInfrastructure})
	if err != nil {
		t.Fatalf("Plan() failed: %v", err)
	}

	var clickhouseTask *Task
	yugabyteBatch, clickhouseBatch := -1, -1

	for batchIdx, batch := range plan.Batches {
		for _, task := range batch {
			if task.Name == "clickhouse" {
				clickhouseTask = task
				clickhouseBatch = batchIdx
			}
			if task.Name == "yugabyte-node-1" {
				yugabyteBatch = batchIdx
			}
		}
	}

	if clickhouseTask == nil {
		t.Fatal("expected clickhouse task in plan")
	}
	if yugabyteBatch == -1 || clickhouseBatch == -1 {
		t.Fatalf("expected both yugabyte-node-1 and clickhouse in plan, got batches: %+v", plan.Batches)
	}
	if clickhouseBatch <= yugabyteBatch {
		t.Fatalf("expected clickhouse after yugabyte-node-1, got yugabyte batch %d clickhouse batch %d", yugabyteBatch, clickhouseBatch)
	}

	if !slices.Contains(clickhouseTask.DependsOn, "yugabyte-node-1") {
		t.Fatalf("expected clickhouse to depend on yugabyte-node-1, got %v", clickhouseTask.DependsOn)
	}
}

func TestPlan_OnlyApplicationsOmitsMissingInfraDeps(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core1": {ExternalIP: "10.0.0.1", WireguardIP: "10.88.0.2", Roles: []string{"control"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "postgres", Host: "core1"},
			Kafka:    &inventory.KafkaConfig{Enabled: true, ClusterID: "c1", Brokers: []inventory.KafkaBroker{{ID: 1, Host: "core1"}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core1"},
			"privateer":     {Enabled: true},
			"bridge":        {Enabled: true, Host: "core1"},
		},
	}

	plan, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: PhaseApplications})
	if err != nil {
		t.Fatalf("Plan(--only applications) failed: %v", err)
	}

	for _, task := range plan.AllTasks {
		for _, dep := range task.DependsOn {
			if dep == "postgres" || dep == "kafka-broker-1" {
				t.Errorf("task %s has infra dep %s that isn't in the --only applications graph", task.Name, dep)
			}
		}
	}
}

func TestPlan_AllPhasesStillHaveCompleteDeps(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core1": {ExternalIP: "10.0.0.1", WireguardIP: "10.88.0.2", Roles: []string{"control"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "postgres", Host: "core1"},
			Kafka:    &inventory.KafkaConfig{Enabled: true, ClusterID: "c1", Brokers: []inventory.KafkaBroker{{ID: 1, Host: "core1"}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core1"},
			"privateer":     {Enabled: true},
			"bridge":        {Enabled: true, Host: "core1"},
		},
	}

	plan, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: PhaseAll})
	if err != nil {
		t.Fatalf("Plan(--only all) failed: %v", err)
	}
	bridgeDeps := map[string]bool{}
	for _, task := range plan.AllTasks {
		if task.Name == "bridge" {
			for _, d := range task.DependsOn {
				bridgeDeps[d] = true
			}
		}
	}
	for _, need := range []string{"quartermaster", "privateer-mesh-core1", "postgres", "kafka-broker-1"} {
		if !bridgeDeps[need] {
			t.Errorf("bridge missing expected dep %q (have %v)", need, bridgeDeps)
		}
	}
}
