package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestBuildServiceEnvVarsNetworkObserverUsesOwnedDatabaseAndPlatformIdentity(t *testing.T) {
	manifest := productionYugabyteManifest()
	manifest.Infrastructure.Postgres.Databases = append(manifest.Infrastructure.Postgres.Databases, inventory.DatabaseConfig{
		Name: "network_observer", Owner: "network_observer",
	})
	manifest.Services["network-observer"] = inventory.ServiceConfig{Enabled: true, Host: "yuga-eu-1", Cluster: "media-eu-1"}
	task := &orchestrator.Task{Name: "network-observer", Type: "network-observer", ServiceID: "network-observer", Host: "yuga-eu-1", ClusterID: "media-eu-1"}
	runtime := map[string]any{"system_tenant_id": "11111111-1111-1111-1111-111111111111"}
	shared := map[string]string{"ARBITRUM_RPC_ENDPOINT": "https://arb.example", "DATABASE_PASSWORD": "test-database-secret"}
	env, err := buildServiceEnvVars(task, manifest, runtime, "", "", shared, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"DATABASE_NAME": "network_observer", "DATABASE_USER": "network_observer",
		"NETWORK_OBSERVER_TENANT_ID":  "11111111-1111-1111-1111-111111111111",
		"NETWORK_OBSERVER_CLUSTER_ID": "media-eu-1", "LIVEPEER_ETH_RPC_URL": "https://arb.example",
	} {
		if got := env[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	manifest.Services["network-observer"] = inventory.ServiceConfig{Enabled: true, Config: map[string]string{
		"NETWORK_OBSERVER_TENANT_ID":  "22222222-2222-2222-2222-222222222222",
		"NETWORK_OBSERVER_CLUSTER_ID": "explicit-cluster", "LIVEPEER_ETH_RPC_URL": "https://explicit.example",
	}}
	env, err = buildServiceEnvVars(task, manifest, runtime, "", "", shared, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range manifest.Services["network-observer"].Config {
		if env[key] != want {
			t.Errorf("explicit %s = %q, want %q", key, env[key], want)
		}
	}
	manifest.Infrastructure.Postgres.Databases[len(manifest.Infrastructure.Postgres.Databases)-1].RuntimeRole = "network_observer_runtime"
	shared["POSTGRES_NETWORK_OBSERVER_RUNTIME_PASSWORD"] = "test-runtime-secret"
	env, err = buildServiceEnvVars(task, manifest, runtime, "", "", shared, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	if env["DATABASE_USER"] != "network_observer_runtime" || env["DATABASE_PASSWORD"] != "test-runtime-secret" {
		t.Fatal("observer did not bind its declared runtime role and scoped runtime credential")
	}
}

func TestBuildServiceEnvVarsNetworkObserverHonorsManifestPort(t *testing.T) {
	manifest := &inventory.Manifest{Profile: "dev", Services: map[string]inventory.ServiceConfig{
		"observer-eu": {Enabled: true, Deploy: "network-observer", Port: 28024},
	}}
	task := &orchestrator.Task{Type: "network-observer", ServiceID: "observer-eu"}
	env, err := buildServiceEnvVars(task, manifest, nil, "", "", nil, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	if got := env["NETWORK_OBSERVER_PORT"]; got != "28024" {
		t.Fatalf("observer listener port = %q, want manifest readiness port 28024", got)
	}
}
