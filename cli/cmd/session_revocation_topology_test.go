package cmd

import (
	"net"
	"strings"
	"testing"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestAuthProjectionConfigurationMatchesDeclaredConsumers(t *testing.T) {
	schema, err := configschema.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range schema.Services {
		if section.Variant != "" {
			continue
		}
		configured := false
		for _, variable := range section.Variables {
			if variable.Key == "AUTH_REVOCATION_KAFKA_BROKERS" {
				configured = true
			}
		}
		if declared := topology.UsesSessionRevocationProjection(section.Service); configured != declared {
			t.Errorf("%s projection configuration=%v but declared consumer=%v; update the service graph and provisioning together", section.Service, configured, declared)
		}
	}
}

func TestStandaloneRevocationConsumersGetAuthorityAndBrokerWithoutSignalman(t *testing.T) {
	for _, service := range []string{"quartermaster", "purser", "periscope-query", "bosun", "lookout", "foghorn", "skipper"} {
		t.Run(service, func(t *testing.T) {
			manifest := aggregatorRegionManifest()
			manifest.Hosts["consumer"] = inventory.Host{WireguardIP: "10.88.0.50", Labels: map[string]string{"region": "eu-west"}}
			manifest.Services[service] = inventory.ServiceConfig{Enabled: true, Host: "consumer"}
			manifest.Services["signalman"] = inventory.ServiceConfig{Enabled: true, Host: "regional-eu-2"}
			env, err := buildServiceEnvVars(&orchestrator.Task{Type: service, ServiceID: service, Host: "consumer"}, manifest, map[string]any{}, "", "", testLoadSharedEnv(t, manifest), nil, "native")
			if err != nil {
				t.Fatal(err)
			}
			brokers := env["AUTH_REVOCATION_KAFKA_BROKERS"]
			if brokers == "" {
				t.Fatal("standalone backend has no revocation broker binding")
			}
			dns := buildPrivateerSeedDNS(manifest, "consumer")
			if len(dns["commodore"]) == 0 {
				t.Fatal("standalone backend cannot resolve its authority")
			}
			if len(dns["signalman"]) != 0 {
				t.Fatal("backend incorrectly received realtime subscription discovery")
			}
			peers := privateerDependencyPeerHosts(manifest, "consumer")
			if _, ok := peers["regional-eu-1"]; !ok {
				t.Fatal("standalone backend lacks its broker mesh peer")
			}
			for _, broker := range strings.Split(brokers, ",") {
				host, _, splitErr := net.SplitHostPort(broker)
				if splitErr != nil {
					t.Fatal(splitErr)
				}
				if net.ParseIP(host) == nil && len(dns[strings.TrimSuffix(host, ".internal")]) == 0 {
					t.Fatalf("generated broker %s is absent from this host's DNS seed", host)
				}
			}
			schema, err := configschema.Load()
			if err != nil {
				t.Fatal(err)
			}
			section, ok := schema.Server(service)
			if !ok {
				t.Fatal("service configuration schema absent")
			}
			found := false
			for _, variable := range section.Variables {
				if variable.Key == "AUTH_REVOCATION_KAFKA_BROKERS" {
					found = true
				}
				if variable.Key == "SIGNALMAN_GRPC_ADDR" {
					t.Fatal("backend still declares a realtime subscription endpoint")
				}
			}
			if !found {
				t.Fatal("broker binding would be discarded by service configuration scoping")
			}
		})
	}
}
