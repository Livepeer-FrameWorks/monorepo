package provisioner

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestNetworkObserverRoleRendersNativeAndComposeReadiness(t *testing.T) {
	cfg := genericServiceRoleConfig("network-observer", ServicePorts["network-observer"])
	config := ServiceConfig{Version: "v0.3.13-rc3", BinaryURL: "https://example.invalid/observer.tar.gz", Image: "ghcr.io/livepeer-frameworks/frameworks-network-observer:v0.3.13-rc3", EnvVars: map[string]string{
		"NETWORK_OBSERVER_SECRET": "test-observer-secret", "NETWORK_OBSERVER_CLUSTER_ID": "platform-cluster", "MIRROR_REGION_PREFIXES": "us-east",
	}}
	host := inventory.Host{Name: "central"}
	config.Mode = "native"
	native, err := serviceVarsBuilderFor(cfg)(context.Background(), host, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatal(err)
	}
	if native["go_service_health_path"] != "/ready" || native["go_service_port"] != 18024 {
		t.Fatalf("native observer gate = %v:%v", native["go_service_port"], native["go_service_health_path"])
	}
	if native["go_service_env"].(map[string]any)["MIRROR_REGION_PREFIXES"] != "us-east" {
		t.Fatal("native observer lost mirror configuration")
	}
	config.Mode = "docker"
	compose, err := serviceVarsBuilderFor(cfg)(context.Background(), host, config, RoleBuildHelpers{})
	if err != nil {
		t.Fatal(err)
	}
	service := compose["compose_stack_service"].(map[string]any)
	if service["health_path"] != "/ready" || service["port"] != 18024 {
		t.Fatalf("Compose observer gate = %v:%v", service["port"], service["health_path"])
	}
}

func TestNetworkObserverDatabaseIsIncludedInReleaseBootstrap(t *testing.T) {
	databases, err := ServiceDatabasesWithBaseline([]SchemaDatabase{{Name: "network_observer"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(databases) != 1 || databases[0].Schema != "network_observer" || databases[0].RuntimeRole != "network_observer_runtime" {
		t.Fatalf("observer bootstrap database = %+v", databases)
	}
	pending, err := PlanServiceDatabaseBootstrap(databases, map[string]ServiceDatabaseState{"network_observer": {}})
	if err != nil || len(pending) != 1 {
		t.Fatalf("missing observer database bootstrap = %+v, %v", pending, err)
	}
}
