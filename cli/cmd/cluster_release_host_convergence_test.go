package cmd

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	infra "github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// multiRegionReleaseManifest is an EU aggregator + US regional topology with a
// MirrorMaker2 link in each direction, Privateer on every host, and Signalman
// and Bridge in both regions.
func multiRegionReleaseManifest() *inventory.Manifest {
	host := func(name, ip, region string) inventory.Host {
		return inventory.Host{Name: name, ExternalIP: ip, WireguardIP: ip, WireguardPublicKey: "key-" + name, Labels: map[string]string{"region": region}}
	}
	topics := []inventory.KafkaTopic{{Name: "service_events", Partitions: 3, ReplicationFactor: 1}}
	return &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"central-eu-1":  host("central-eu-1", "10.88.0.10", "eu-west"),
			"regional-eu-1": host("regional-eu-1", "10.88.1.11", "eu-west"),
			"regional-us-1": host("regional-us-1", "10.88.10.11", "us-east"),
			"edge-eu-1":     {Name: "edge-eu-1", ExternalIP: "203.0.113.5", Roles: []string{infra.NodeTypeEdge}},
		},
		Services: map[string]inventory.ServiceConfig{
			"privateer": {Enabled: true, Hosts: []string{"central-eu-1", "regional-eu-1", "regional-us-1"}},
			"signalman": {Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}},
			"bridge":    {Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-kafka",
				RegionID:  "eu-west",
				Role:      "aggregator",
				Brokers:   []inventory.KafkaBroker{{Host: "regional-eu-1", ID: 1, Port: 9092}},
				Topics:    topics,
				Regional: []inventory.RegionalKafkaCluster{{
					RegionID:  "us-east",
					ClusterID: "us-kafka",
					Brokers:   []inventory.KafkaBroker{{Host: "regional-us-1", ID: 11, Port: 9092}},
					Topics:    topics,
				}},
				MirrorMaker: &inventory.KafkaMirrorMakerConfig{
					Enabled: true,
					Links: []inventory.KafkaMirrorLink{
						{Source: "us-east", Target: "eu-west", Hosts: []string{"regional-eu-1"}},
						{Source: "eu-west", Target: "us-east", Hosts: []string{"regional-us-1"}},
					},
				},
			},
		},
	}
}

func TestReleaseHostConvergenceOrdersMeshTopicsThenMirrorMaker(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	steps := planReleaseHostConvergence(plan, manifest)
	var got []string
	for _, step := range steps {
		got = append(got, step.Kind+":"+step.Label)
	}
	want := []string{
		"privateer:central-eu-1",
		"privateer:regional-eu-1",
		"privateer:regional-us-1",
		"kafka-topics:eu-west, us-east",
		"kafka-mirrormaker:regional-eu-1",
		"kafka-mirrormaker:regional-us-1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("host convergence steps = %v, want %v", got, want)
	}
	for _, step := range steps {
		if step.Kind == releaseHostStepKafkaTopics {
			if step.Task != nil {
				t.Fatalf("kafka topic step carries a host task %v; it must never provision a broker", step.Task.Name)
			}
			continue
		}
		if step.Task == nil || step.Task.Host != step.Label {
			t.Fatalf("step %s:%s has task %+v, want the planner task for that host", step.Kind, step.Label, step.Task)
		}
		if step.Task.Type == "kafka" || step.Task.Type == "kafka-controller" {
			t.Fatalf("host convergence must not include broker or controller tasks, got %s", step.Task.Name)
		}
	}

	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", steps)
	for _, line := range []string{
		"Privateer binary, seed peers, and seed DNS: central-eu-1 -> regional-eu-1 -> regional-us-1",
		"Kafka topics created when missing (no broker restart): eu-west, us-east",
		"MirrorMaker2 workers and JMX exporter: regional-eu-1 -> regional-us-1",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("plan output missing %q:\n%s", line, out.String())
		}
	}
}

func TestReleaseHostConvergenceEmptyWithoutMeshOrKafka(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile:  "dev",
		Hosts:    map[string]inventory.Host{"core-1": {Name: "core-1", ExternalIP: "10.0.0.1"}},
		Services: map[string]inventory.ServiceConfig{"bridge": {Enabled: true, Host: "core-1"}},
	}
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if steps := planReleaseHostConvergence(plan, manifest); len(steps) != 0 {
		t.Fatalf("steps = %+v, want none", steps)
	}
	var out bytes.Buffer
	writeReleaseHostConvergencePlan(&out, "1. pre-upgrade host convergence", nil)
	if !strings.Contains(out.String(), "1. pre-upgrade host convergence: none") {
		t.Fatalf("plan output = %q", out.String())
	}
}

// release apply upgrades services in the order collectUpgradeableServices reads
// from the plan, so every Signalman must precede Bridge there.
func TestReleaseUpgradeOrderPutsSignalmanBeforeBridge(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	services := collectUpgradeableServices(plan)
	signalman, bridge := slices.Index(services, "signalman"), slices.Index(services, "bridge")
	if signalman < 0 || bridge < 0 || signalman > bridge {
		t.Fatalf("release upgrade order = %v, want signalman before bridge", services)
	}
}

func TestStaleKafkaMirrorMakerWorkerCandidates(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	if got, want := staleKafkaMirrorMakerWorkerCandidates(manifest), []string{"central-eu-1"}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v (link workers and edge hosts excluded)", got, want)
	}

	manifest.Infrastructure.Kafka.MirrorMaker.Enabled = false
	if got, want := staleKafkaMirrorMakerWorkerCandidates(manifest), []string{"central-eu-1", "regional-eu-1", "regional-us-1"}; !slices.Equal(got, want) {
		t.Fatalf("candidates with MirrorMaker2 disabled = %v, want %v", got, want)
	}

	manifest.Infrastructure.Kafka = nil
	if got := staleKafkaMirrorMakerWorkerCandidates(manifest); len(got) != 0 {
		t.Fatalf("candidates without Kafka = %v, want none", got)
	}
}

func TestReconcileStaleKafkaMirrorMakerWorkersRemovesOnlyUndeclaredWorkers(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	// regional-us-1 no longer serves the eu-west -> us-east link; the worker
	// moves to a new US host.
	manifest.Hosts["regional-us-2"] = inventory.Host{Name: "regional-us-2", ExternalIP: "10.88.10.12", Labels: map[string]string{"region": "us-east"}}
	manifest.Infrastructure.Kafka.MirrorMaker.Links[1].Hosts = []string{"regional-us-2"}

	running := map[string]bool{"regional-eu-1": true, "regional-us-1": true, "regional-us-2": true}
	var probed []string
	probe := func(_ context.Context, host inventory.Host) (bool, error) {
		probed = append(probed, host.Name)
		return running[host.Name], nil
	}

	var dryRunOut bytes.Buffer
	var removed []string
	remove := func(_ context.Context, host inventory.Host) error {
		removed = append(removed, host.Name)
		return nil
	}
	if err := reconcileStaleKafkaMirrorMakerWorkers(context.Background(), &dryRunOut, manifest, probe, remove, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("dry-run removed %v", removed)
	}
	if !strings.Contains(dryRunOut.String(), "kafka-mirrormaker on regional-us-1: would stop and remove the worker") {
		t.Fatalf("dry-run output = %q", dryRunOut.String())
	}
	for _, desired := range []string{"regional-eu-1", "regional-us-2"} {
		if slices.Contains(probed, desired) {
			t.Fatalf("probed declared link worker %s; probes = %v", desired, probed)
		}
	}

	var out bytes.Buffer
	if err := reconcileStaleKafkaMirrorMakerWorkers(context.Background(), &out, manifest, probe, remove, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !slices.Equal(removed, []string{"regional-us-1"}) {
		t.Fatalf("removed = %v, want [regional-us-1]", removed)
	}
}

func TestReconcileStaleKafkaMirrorMakerWorkersFailsClosedOnProbeError(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	probe := func(context.Context, inventory.Host) (bool, error) {
		return false, errors.New("ssh: connection refused")
	}
	remove := func(context.Context, inventory.Host) error {
		t.Fatal("remove must not run when a probe fails")
		return nil
	}
	err := reconcileStaleKafkaMirrorMakerWorkers(context.Background(), &bytes.Buffer{}, manifest, probe, remove, false)
	if err == nil || !strings.Contains(err.Error(), "central-eu-1") {
		t.Fatalf("err = %v, want probe failure naming the host", err)
	}
}
