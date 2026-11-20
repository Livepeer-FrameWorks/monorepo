package orchestrator

import (
	"context"
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestPlan_FoghornDeploysAfterPublicEventConsumers(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"eu": {}, "us": {}, "core-a": {}, "core-b": {},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-eu":  {Enabled: true, Deploy: "foghorn", Cluster: "media-eu", Host: "eu"},
			"chandler-eu": {Enabled: true, Deploy: "chandler", Cluster: "media-eu", Host: "eu"},
			"foghorn-us":  {Enabled: true, Deploy: "foghorn", Cluster: "media-us", Host: "us"},
			"chandler-us": {Enabled: true, Deploy: "chandler", Cluster: "media-us", Host: "us"},
			"signalman":   {Enabled: true, Hosts: []string{"eu", "us"}},
			"bosun-core":  {Enabled: true, Deploy: "bosun", Hosts: []string{"core-a", "core-b"}},
			"ingest-eu":   {Enabled: true, Deploy: "periscope-ingest", Host: "eu"},
			"ingest-us":   {Enabled: true, Deploy: "periscope-ingest", Host: "us"},
			"bosun-off":   {Enabled: false, Deploy: "bosun", Host: "core-a"},
		},
	}
	consumers := []string{"signalman@eu", "signalman@us", "bosun-core@core-a", "bosun-core@core-b", "ingest-eu", "ingest-us"}
	for _, phase := range []Phase{PhaseApplications, PhaseAll} {
		t.Run(string(phase), func(t *testing.T) {
			plan, err := NewPlanner(manifest).Plan(context.Background(), ProvisionOptions{Phase: phase})
			if err != nil {
				t.Fatal(err)
			}
			batchOf := map[string]int{}
			for index, batch := range plan.Batches {
				for _, task := range batch {
					batchOf[task.Name] = index
				}
			}
			producers := 0
			for _, task := range plan.AllTasks {
				if task.Type != "foghorn" {
					continue
				}
				producers++
				for _, consumer := range consumers {
					if !slices.Contains(task.DependsOn, consumer) {
						t.Errorf("%s does not wait for public-event consumer %s: dependencies %v", task.Name, consumer, task.DependsOn)
					}
					index, exists := batchOf[consumer]
					if !exists || batchOf[task.Name] <= index {
						t.Errorf("%s batch %d must follow consumer %s batch %d (present=%t)", task.Name, batchOf[task.Name], consumer, index, exists)
					}
				}
				if slices.Contains(task.DependsOn, "bosun-off") {
					t.Errorf("%s waits for disabled consumer", task.Name)
				}
			}
			if producers != 2 {
				t.Fatalf("got %d Foghorn producers, want 2", producers)
			}
		})
	}
}
