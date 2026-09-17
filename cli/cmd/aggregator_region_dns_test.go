package cmd

import (
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// aggregatorRegionManifest mirrors production placement: central writers on an
// EU control host, Decklog in both regions, EU as the Kafka aggregator.
func aggregatorRegionManifest() *inventory.Manifest {
	host := func(ip, region string) inventory.Host {
		return inventory.Host{WireguardIP: ip, WireguardPublicKey: "key-" + ip, Labels: map[string]string{"region": region}}
	}
	return &inventory.Manifest{
		Profile:    "dev",
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Region: "eu-west"},
			"media-us-1": {Region: "us-east"},
		},
		Hosts: map[string]inventory.Host{
			"central-eu-1":  host("10.88.0.10", "eu-west"),
			"regional-eu-1": host("10.88.1.11", "eu-west"),
			"regional-eu-2": host("10.88.1.12", "eu-west"),
			"regional-us-1": host("10.88.10.11", "us-east"),
			"regional-us-2": host("10.88.10.12", "us-east"),
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "central-eu-1"},
			"commodore":     {Enabled: true, Host: "central-eu-1"},
			"purser":        {Enabled: true, Host: "central-eu-1"},
			"decklog":       {Enabled: true, Hosts: []string{"regional-eu-1", "regional-eu-2", "regional-us-1", "regional-us-2"}},
			"bridge":        {Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-kafka",
				RegionID:  "eu-west",
				Role:      "aggregator",
				Brokers:   []inventory.KafkaBroker{{Host: "regional-eu-1", ID: 1, Port: 9092}},
				Regional: []inventory.RegionalKafkaCluster{
					{RegionID: "us-east", ClusterID: "us-kafka", Brokers: []inventory.KafkaBroker{{Host: "regional-us-1", ID: 11, Port: 9092}}},
				},
			},
		},
	}
}

func TestCentralWritersResolveOnlyAggregatorRegionDecklog(t *testing.T) {
	manifest := aggregatorRegionManifest()
	usDecklogIPs := []string{"10.88.10.11", "10.88.10.12"}

	dns := buildPrivateerSeedDNS(manifest, "central-eu-1")
	if got, want := dns["decklog.eu-west"], []string{"10.88.1.11", "10.88.1.12"}; !slices.Equal(got, want) {
		t.Fatalf("central decklog.eu-west DNS = %v, want EU Decklog replicas %v", got, want)
	}
	for record, ips := range dns {
		if !strings.HasPrefix(record, "decklog") {
			continue
		}
		for _, ip := range ips {
			if slices.Contains(usDecklogIPs, ip) {
				t.Fatalf("central DNS record %s resolves US Decklog %s", record, ip)
			}
		}
	}

	peers := privateerDependencyPeerHosts(manifest, "central-eu-1")
	for _, want := range []string{"regional-eu-1", "regional-eu-2"} {
		if _, ok := peers[want]; !ok {
			t.Fatalf("central mesh peers missing aggregator-region Decklog host %s: %v", want, sortedKeys(peers))
		}
	}
	for _, unwanted := range []string{"regional-us-1", "regional-us-2"} {
		if _, ok := peers[unwanted]; ok {
			t.Fatalf("central mesh peers include US Decklog host %s: %v", unwanted, sortedKeys(peers))
		}
	}
}

// Seeds converge before the service upgrades in `release apply`, so they carry
// both the names the upgraded services dial and the names the previous release
// dials: decklog.<aggregator-region> next to the plain decklog alias on central
// writers, and signalman.<region> next to the local signalman alias on Bridge
// hosts.
func TestPrivateerSeedsResolveCurrentAndPreviousReleaseNames(t *testing.T) {
	manifest := aggregatorRegionManifest()
	manifest.Services["signalman"] = inventory.ServiceConfig{Enabled: true, Hosts: []string{"regional-eu-1", "regional-eu-2", "regional-us-1", "regional-us-2"}}
	euRegional := []string{"10.88.1.11", "10.88.1.12"}
	usRegional := []string{"10.88.10.11", "10.88.10.12"}

	central := buildPrivateerSeedDNS(manifest, "central-eu-1")
	if got := central["decklog.eu-west"]; !slices.Equal(got, euRegional) {
		t.Fatalf("central decklog.eu-west DNS = %v, want %v", got, euRegional)
	}
	if got := central["decklog"]; !slices.Equal(got, euRegional) {
		t.Fatalf("central decklog DNS = %v, want aggregator-region replicas %v for services on the previous release", got, euRegional)
	}

	bridgeHost := buildPrivateerSeedDNS(manifest, "regional-eu-1")
	if got := bridgeHost["signalman.eu-west"]; !slices.Equal(got, euRegional) {
		t.Fatalf("bridge host signalman.eu-west DNS = %v, want %v", got, euRegional)
	}
	if got := bridgeHost["signalman.us-east"]; !slices.Equal(got, usRegional) {
		t.Fatalf("bridge host signalman.us-east DNS = %v, want %v", got, usRegional)
	}
	peers := privateerDependencyPeerHosts(manifest, "regional-eu-1")
	for _, want := range []string{"regional-us-1", "regional-us-2"} {
		if _, ok := peers[want]; !ok {
			t.Fatalf("bridge host mesh peers missing regional Signalman host %s: %v", want, sortedKeys(peers))
		}
	}

	if got := central["signalman.us-east"]; len(got) != 0 {
		t.Fatalf("central signalman.us-east DNS = %v, want none on a host with no Signalman consumer", got)
	}
}

func TestDecklogAddressFollowsDependencyScope(t *testing.T) {
	manifest := aggregatorRegionManifest()
	envFor := func(task *orchestrator.Task) map[string]string {
		t.Helper()
		env, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil, "native")
		if err != nil {
			t.Fatalf("buildServiceEnvVars %s on %s: %v", task.Type, task.Host, err)
		}
		return env
	}

	for _, service := range []string{"commodore", "purser", "quartermaster"} {
		env := envFor(&orchestrator.Task{Type: service, ServiceID: service, Host: "central-eu-1"})
		if got := env["DECKLOG_GRPC_ADDR"]; !strings.HasPrefix(got, "decklog.eu-west.internal:") {
			t.Fatalf("%s DECKLOG_GRPC_ADDR = %q, want the aggregator-region pool", service, got)
		}
	}

	bridge := envFor(&orchestrator.Task{Type: "bridge", ServiceID: "bridge", Host: "regional-us-1", ClusterID: "media-us-1"})
	if got := bridge["DECKLOG_GRPC_ADDR"]; !strings.HasPrefix(got, "decklog.internal:") {
		t.Fatalf("US bridge DECKLOG_GRPC_ADDR = %q, want its cluster-local Decklog", got)
	}
}

func TestAggregatorPinnedServiceRejectedOutsideAggregatorRegion(t *testing.T) {
	manifest := aggregatorRegionManifest()
	manifest.Services["periscope-ingest"] = inventory.ServiceConfig{Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}}
	build := func(host string) error {
		task := &orchestrator.Task{Type: "periscope-ingest", ServiceID: "periscope-ingest", Host: host}
		_, err := buildServiceEnvVars(task, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil, "native")
		return err
	}

	if err := build("regional-eu-1"); err != nil {
		t.Fatalf("periscope-ingest in the aggregator region: %v", err)
	}
	err := build("regional-us-1")
	if err == nil || !strings.Contains(err.Error(), `binds the aggregator Kafka in region "eu-west"`) {
		t.Fatalf("periscope-ingest in us-east err = %v, want aggregator placement rejection", err)
	}
}
