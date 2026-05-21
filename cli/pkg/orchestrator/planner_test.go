package orchestrator

import (
	"context"
	"slices"
	"sort"
	"strings"
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

func TestEffectivePrivateerHostsForManifestAddsMetricsHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"mesh-1":    {ExternalIP: "10.0.0.1"},
			"metrics-1": {ExternalIP: "10.0.0.2"},
			"yuga-1":    {ExternalIP: "10.0.0.3"},
			"ch-1":      {ExternalIP: "10.0.0.4"},
			"vm-1":      {ExternalIP: "10.0.0.5"},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {Enabled: true, Hosts: []string{"mesh-1"}},
		},
		Observability: map[string]inventory.ServiceConfig{
			"vmagent": {Enabled: true, Hosts: []string{"metrics-1"}},
			"vmauth":  {Enabled: true, Host: "mesh-1"},
			"victoriametrics": {
				Enabled: true,
				Host:    "vm-1",
			},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Nodes:   []inventory.PostgresNode{{Host: "yuga-1"}},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Host:    "ch-1",
			},
		},
	}

	got := EffectivePrivateerHostsForManifest(manifest.Services["privateer"], manifest)
	want := []string{"ch-1", "mesh-1", "metrics-1", "vm-1", "yuga-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("EffectivePrivateerHostsForManifest = %v, want %v", got, want)
	}
}

func TestEffectiveVMAgentHostsAddsMetricsInfrastructureToExplicitHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"central-1": {ExternalIP: "10.0.0.1"},
			"yuga-1":    {ExternalIP: "10.0.0.2"},
			"yuga-2":    {ExternalIP: "10.0.0.3"},
			"ch-1":      {ExternalIP: "10.0.0.4"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true,
				Engine:  "yugabyte",
				Nodes: []inventory.PostgresNode{
					{Host: "yuga-1"},
					{Host: "yuga-2"},
				},
			},
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true,
				Host:    "ch-1",
			},
		},
	}

	got := EffectiveVMAgentHosts(inventory.ServiceConfig{Hosts: []string{"central-1"}}, manifest)
	want := []string{"central-1", "ch-1", "yuga-1", "yuga-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("EffectiveVMAgentHosts = %v, want %v", got, want)
	}
}

func TestEffectiveVMAgentHostsDefaultsToAllHosts(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"central-1":  {ExternalIP: "10.0.0.1"},
			"regional-1": {ExternalIP: "10.0.0.2"},
		},
	}

	got := EffectiveVMAgentHosts(inventory.ServiceConfig{}, manifest)
	want := []string{"central-1", "regional-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("EffectiveVMAgentHosts = %v, want %v", got, want)
	}
}

func TestPlan_RedisDuplicateNamesAreClusterScoped(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "10.0.0.1"},
			"regional-eu-2": {ExternalIP: "10.0.0.2"},
			"regional-us-1": {ExternalIP: "10.0.1.1"},
			"regional-us-2": {ExternalIP: "10.0.1.2"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true,
				Instances: []inventory.RedisInstance{
					{Name: "foghorn", Cluster: "media-eu-1", Mode: "sentinel", Host: "regional-eu-1", ReplicaHosts: []string{"regional-eu-2"}},
					{Name: "foghorn", Cluster: "media-us-1", Mode: "sentinel", Host: "regional-us-1", ReplicaHosts: []string{"regional-us-2"}},
				},
			},
		},
	}

	plan, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: PhaseInfrastructure})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}

	names := map[string]bool{}
	for _, batch := range plan.Batches {
		for _, task := range batch {
			names[task.Name] = true
		}
	}
	for _, name := range []string{
		"redis-foghorn-media-eu-1",
		"redis-foghorn-media-us-1",
		"redis-foghorn-media-eu-1-replica-regional-eu-2",
		"redis-foghorn-media-us-1-replica-regional-us-2",
	} {
		if !names[name] {
			t.Fatalf("missing Redis task %s; got %#v", name, names)
		}
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

func TestPlan_KafkaMirrorMakerHostsFanOut(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "10.0.0.1", Labels: map[string]string{"region": "eu-west"}},
			"regional-eu-2": {ExternalIP: "10.0.0.2", Labels: map[string]string{"region": "eu-west"}},
			"regional-eu-3": {ExternalIP: "10.0.0.3", Labels: map[string]string{"region": "eu-west"}},
			"regional-us-1": {ExternalIP: "10.0.1.1", Labels: map[string]string{"region": "us-east"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:  true,
				RegionID: "eu-west",
				Role:     "aggregator",
				Brokers: []inventory.KafkaBroker{
					{Host: "regional-eu-1", ID: 1},
					{Host: "regional-eu-2", ID: 2},
					{Host: "regional-eu-3", ID: 3},
				},
				Regional: []inventory.RegionalKafkaCluster{
					{
						RegionID: "us-east",
						Brokers:  []inventory.KafkaBroker{{Host: "regional-us-1", ID: 11}},
					},
				},
				MirrorMaker: &inventory.KafkaMirrorMakerConfig{
					Enabled: true,
					Hosts:   []string{"regional-eu-1", "regional-eu-2", "regional-eu-3"},
				},
			},
		},
	}

	plan, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: PhaseInfrastructure})
	if err != nil {
		t.Fatalf("Plan() failed: %v", err)
	}

	got := map[string]*Task{}
	for _, task := range plan.AllTasks {
		if task.Type == "kafka-mirrormaker" {
			got[task.Host] = task
		}
	}
	for _, host := range []string{"regional-eu-1", "regional-eu-2", "regional-eu-3"} {
		task := got[host]
		if task == nil {
			t.Fatalf("missing MirrorMaker task on %s; got %#v", host, got)
		}
		if !slices.Contains(task.DependsOn, "kafka-broker-eu-west-1") || !slices.Contains(task.DependsOn, "kafka-broker-us-east-11") {
			t.Fatalf("MirrorMaker task %s deps = %v, want all Kafka brokers", task.Name, task.DependsOn)
		}
	}

	lastKafkaBatch := -1
	firstMMBatch := -1
	for batchIdx, batch := range plan.Batches {
		for _, task := range batch {
			switch task.Type {
			case "kafka":
				lastKafkaBatch = batchIdx
			case "kafka-mirrormaker":
				if firstMMBatch == -1 {
					firstMMBatch = batchIdx
				}
			}
		}
	}
	if lastKafkaBatch == -1 || firstMMBatch == -1 {
		t.Fatalf("expected kafka and MirrorMaker batches, got %#v", plan.Batches)
	}
	if firstMMBatch <= lastKafkaBatch {
		t.Fatalf("MirrorMaker batch %d must be after final Kafka broker batch %d", firstMMBatch, lastKafkaBatch)
	}
}

func TestPlan_KafkaMirrorMakerRejectsNonAggregatorHost(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {ExternalIP: "10.0.0.1", Labels: map[string]string{"region": "eu-west"}},
			"regional-us-1": {ExternalIP: "10.0.1.1", Labels: map[string]string{"region": "us-east"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:  true,
				RegionID: "eu-west",
				Role:     "aggregator",
				Brokers:  []inventory.KafkaBroker{{Host: "regional-eu-1", ID: 1}},
				Regional: []inventory.RegionalKafkaCluster{
					{
						RegionID: "us-east",
						Brokers:  []inventory.KafkaBroker{{Host: "regional-us-1", ID: 11}},
					},
				},
				MirrorMaker: &inventory.KafkaMirrorMakerConfig{
					Enabled: true,
					Hosts:   []string{"regional-us-1"},
				},
			},
		},
	}

	_, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: PhaseInfrastructure})
	if err == nil {
		t.Fatal("Plan() succeeded with MirrorMaker outside aggregator region")
	}
	if !strings.Contains(err.Error(), "want aggregator region \"eu-west\"") {
		t.Fatalf("Plan() error = %v", err)
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
	for _, need := range []string{"quartermaster", "privateer-mesh-core1"} {
		if !bridgeDeps[need] {
			t.Errorf("bridge missing expected dep %q (have %v)", need, bridgeDeps)
		}
	}
	if bridgeDeps["postgres"] {
		t.Errorf("bridge unexpectedly depends on postgres despite no direct database dependency (have %v)", bridgeDeps)
	}
	if bridgeDeps["kafka-broker-1"] {
		t.Errorf("bridge unexpectedly depends on kafka despite no direct Kafka dependency (have %v)", bridgeDeps)
	}
}

func TestPlan_SkipperDependsOnBridge(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core1": {ExternalIP: "10.0.0.1", WireguardIP: "10.88.0.2", Roles: []string{"control"}},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core1"},
			"bridge":        {Enabled: true, Host: "core1"},
			"skipper":       {Enabled: true, Host: "core1"},
		},
	}

	plan, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: PhaseApplications})
	if err != nil {
		t.Fatalf("Plan(--only applications) failed: %v", err)
	}

	var skipper *Task
	for _, task := range plan.AllTasks {
		if task.Name == "skipper" {
			skipper = task
			break
		}
	}
	if skipper == nil {
		t.Fatal("expected skipper task")
	}
	if !slices.Contains(skipper.DependsOn, "bridge") {
		t.Fatalf("expected skipper to depend on bridge, got %v", skipper.DependsOn)
	}
}
