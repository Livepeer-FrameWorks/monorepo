package orchestrator

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestNetworkObserverPlanWaitsForDatabaseAggregatorAndDecklog(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"central": {}, "regional": {}},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Host: "central"},
			Kafka:    &inventory.KafkaConfig{Enabled: true, Role: "aggregator", Brokers: []inventory.KafkaBroker{{Host: "central", ID: 1}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"decklog":          {Enabled: true, Host: "central"},
			"observer-central": {Enabled: true, Deploy: "network-observer", Host: "central"},
		},
	}
	p := NewPlanner(manifest)
	graph := NewDependencyGraph()
	if err := p.addInfrastructureTasks(graph); err != nil {
		t.Fatal(err)
	}
	if err := p.addApplicationTasks(graph); err != nil {
		t.Fatal(err)
	}
	observer := graph.tasks["observer-central"]
	if observer == nil {
		t.Fatal("observer task absent")
	}
	for _, name := range []string{"postgres", "kafka-broker-1", "decklog"} {
		if !slices.Contains(observer.DependsOn, name) {
			t.Errorf("observer dependencies = %v, want %s", observer.DependsOn, name)
		}
	}
}
