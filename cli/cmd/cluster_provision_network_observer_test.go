package cmd

import (
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/clusterderive"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
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

// The Controller is fixed per chain: the observer resolves it from its chain ID
// (go-livepeer's network defaults), an explicit value wins, and a chain with no
// known Controller is refused rather than pointed at another chain's contract.
func TestRenderNetworkObserverEnvDerivesControllerFromChain(t *testing.T) {
	dev := &inventory.Manifest{Profile: "dev"}
	for _, tc := range []struct{ chainID, want string }{
		{"", "0xD8E8328501E9645d16Cf49539efC04f734606ee4"},
		{"42161", "0xD8E8328501E9645d16Cf49539efC04f734606ee4"},
		{"1", "0xf96d54e490317c557a967abfa5d6e33006be69b3"},
	} {
		env := map[string]string{"LIVEPEER_NETWORK_ID": tc.chainID}
		if err := renderNetworkObserverEnv(dev, env); err != nil {
			t.Fatalf("chain %q: %v", tc.chainID, err)
		}
		if env["LIVEPEER_CONTROLLER"] != tc.want {
			t.Fatalf("chain %q controller = %q, want %q", tc.chainID, env["LIVEPEER_CONTROLLER"], tc.want)
		}
	}
	explicit := map[string]string{"LIVEPEER_NETWORK_ID": "421614", "LIVEPEER_CONTROLLER": "0x1111111111111111111111111111111111111111"}
	if err := renderNetworkObserverEnv(dev, explicit); err != nil || explicit["LIVEPEER_CONTROLLER"] != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("explicit controller = %q, err %v; want it preserved", explicit["LIVEPEER_CONTROLLER"], err)
	}
	err := renderNetworkObserverEnv(dev, map[string]string{"LIVEPEER_NETWORK_ID": "421614"})
	if err == nil || !strings.Contains(err.Error(), "421614") || !strings.Contains(err.Error(), "LIVEPEER_CONTROLLER") {
		t.Fatalf("unknown chain err = %v, want refusal naming the chain and LIVEPEER_CONTROLLER", err)
	}
}

// Probers reach the observer only through Bridge's relay, so the observer
// takes the forwarded source address from exactly the Bridge hosts' mesh
// addresses.
func TestBuildServiceEnvVarsNetworkObserverTrustsBridgeRelays(t *testing.T) {
	manifest := &inventory.Manifest{Profile: "dev",
		Hosts: map[string]inventory.Host{
			"core-1": {WireguardIP: "10.88.0.10"}, "core-2": {WireguardIP: "10.88.0.11"}, "edge-1": {WireguardIP: "10.88.0.50"},
		},
		Services: map[string]inventory.ServiceConfig{
			"network-observer": {Enabled: true, Host: "core-1"},
			"bridge":           {Enabled: true, Hosts: []string{"core-2", "core-1"}},
			"foghorn":          {Enabled: true, Host: "edge-1"},
		}}
	task := &orchestrator.Task{Type: "network-observer", ServiceID: "network-observer", Host: "core-1"}
	env, err := buildServiceEnvVars(task, manifest, nil, "", "", nil, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	if got := env["NETWORK_OBSERVER_TRUSTED_PROXY_CIDRS"]; got != "10.88.0.10/32,10.88.0.11/32" {
		t.Fatalf("trusted relays = %q, want exactly the Bridge hosts' mesh addresses", got)
	}
	manifest.Services["network-observer"] = inventory.ServiceConfig{Enabled: true, Host: "core-1", Config: map[string]string{"NETWORK_OBSERVER_TRUSTED_PROXY_CIDRS": "10.0.0.0/8"}}
	env, err = buildServiceEnvVars(task, manifest, nil, "", "", nil, nil, "native")
	if err != nil || env["NETWORK_OBSERVER_TRUSTED_PROXY_CIDRS"] != "10.0.0.0/8" {
		t.Fatalf("explicit trusted proxies = %q, err %v", env["NETWORK_OBSERVER_TRUSTED_PROXY_CIDRS"], err)
	}
}

// The observer serves Bridge over its internal-CA listener, so provisioning
// gives it a leaf and Privateer renews that leaf on its host.
func TestNetworkObserverGetsAnInternalServiceLeaf(t *testing.T) {
	manifest := &inventory.Manifest{Profile: "dev", Services: map[string]inventory.ServiceConfig{
		"network-observer": {Enabled: true, Host: "core-1"},
	}}
	task := &orchestrator.Task{Type: "network-observer", ServiceID: "network-observer", Host: "core-1"}
	env, err := buildServiceEnvVars(task, manifest, nil, "", "", nil, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	if env["GRPC_TLS_CERT_PATH"] != "/etc/frameworks/pki/services/network-observer/tls.crt" || env["GRPC_TLS_KEY_PATH"] != "/etc/frameworks/pki/services/network-observer/tls.key" {
		t.Fatalf("observer leaf = (%q, %q), want the network-observer internal leaf", env["GRPC_TLS_CERT_PATH"], env["GRPC_TLS_KEY_PATH"])
	}
	if got := internalGRPCLeafServicesForHost(manifest, "core-1"); len(got) != 1 || got[0] != "network-observer" {
		t.Fatalf("Privateer leaves on the observer host = %v, want [network-observer]", got)
	}
}

// One SOPS-held secret reaches both ends of the observer API, a production
// manifest that declares the observer refuses to render without it, and no
// unrelated service receives it.
func TestBuildServiceEnvVarsNetworkObserverSecretReachesObserverAndBridgeOnly(t *testing.T) {
	missing := contractProductionManifest(t, "NETWORK_OBSERVER_SECRET=change-me")
	missingShared := testLoadSharedEnv(t, missing)
	for _, service := range []string{"network-observer", "bridge"} {
		_, err := buildServiceEnvVars(contractTask(service), missing, map[string]any{}, "", "", missingShared, nil, "native")
		if err == nil || !strings.Contains(err.Error(), "NETWORK_OBSERVER_SECRET") || !strings.Contains(err.Error(), "generate-network-observer") {
			t.Fatalf("%s without observer secret: err = %v, want refusal naming the secret and its generator", service, err)
		}
	}

	manifest := contractProductionManifest(t)
	shared := testLoadSharedEnv(t, manifest)
	observer, err := buildServiceEnvVars(contractTask("network-observer"), manifest, map[string]any{}, "", "", shared, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := buildServiceEnvVars(contractTask("bridge"), manifest, map[string]any{}, "", "", shared, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	if observer["NETWORK_OBSERVER_SECRET"] != "test-network-observer-secret" || bridge["NETWORK_OBSERVER_SECRET"] != observer["NETWORK_OBSERVER_SECRET"] {
		t.Fatalf("observer secret %q and Bridge secret %q must be the shared value", observer["NETWORK_OBSERVER_SECRET"], bridge["NETWORK_OBSERVER_SECRET"])
	}
	commodore, err := buildServiceEnvVars(contractTask("commodore"), manifest, map[string]any{}, "", "", shared, nil, "native")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := commodore["NETWORK_OBSERVER_SECRET"]; ok {
		t.Fatal("an unrelated service received the observer secret")
	}
}

// Bridge reads the declared observer over the mesh, at its aggregator-region
// service alias on the internal-CA listener, with the observer's own secret,
// namespace and tenant as the published scope.
func TestRenderBridgeNetworkObservationEnvFollowsDeclaredObserver(t *testing.T) {
	manifest := &inventory.Manifest{Profile: "production", RootDomain: "frameworks.network", Services: map[string]inventory.ServiceConfig{
		"bridge":           {Enabled: true, Host: "core-1"},
		"navigator":        {Enabled: true, Host: "core-1"},
		"network-observer": {Enabled: true, Host: "core-1"},
	}}
	env := map[string]string{"NETWORK_OBSERVER_SECRET": "observer-secret", "SYSTEM_TENANT_ID": "11111111-1111-1111-1111-111111111111"}
	if err := renderBridgeNetworkObservationEnv(manifest, env); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"NETWORK_OBSERVER_URL":      "https://network-observer.internal:18034",
		"NETWORK_PUBLIC_TENANT_IDS": "11111111-1111-1111-1111-111111111111",
	} {
		if env[key] != want {
			t.Fatalf("%s = %q, want %q", key, env[key], want)
		}
	}

	manifest.Services["network-observer"] = inventory.ServiceConfig{Enabled: true, Host: "core-1", Config: map[string]string{
		"NETWORK_OBSERVER_TENANT_ID": "22222222-2222-2222-2222-222222222222", "NETWORK_OBSERVER_SECRET": "config-secret",
		"LIVEPEER_NETWORK_ID": "1", "NETWORK_OBSERVER_ENVIRONMENT": "lab",
	}}
	env = map[string]string{"SYSTEM_TENANT_ID": "11111111-1111-1111-1111-111111111111"}
	if err := renderBridgeNetworkObservationEnv(manifest, env); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"NETWORK_PUBLIC_TENANT_IDS": "22222222-2222-2222-2222-222222222222", "NETWORK_OBSERVER_SECRET": "config-secret",
		"LIVEPEER_NETWORK_ID": "1", "NETWORK_OBSERVER_ENVIRONMENT": "lab",
	} {
		if env[key] != want {
			t.Fatalf("observer-configured %s = %q, want %q", key, env[key], want)
		}
	}

	explicit := map[string]string{
		"NETWORK_OBSERVER_URL": "https://observer.example.net", "NETWORK_OBSERVER_SECRET": "bridge-secret",
		"NETWORK_PUBLIC_TENANT_IDS": "33333333-3333-3333-3333-333333333333", "SYSTEM_TENANT_ID": "11111111-1111-1111-1111-111111111111",
	}
	if err := renderBridgeNetworkObservationEnv(manifest, explicit); err != nil {
		t.Fatal(err)
	}
	if explicit["NETWORK_OBSERVER_URL"] != "https://observer.example.net" || explicit["NETWORK_PUBLIC_TENANT_IDS"] != "33333333-3333-3333-3333-333333333333" || explicit["NETWORK_OBSERVER_SECRET"] != "bridge-secret" {
		t.Fatalf("explicit Bridge values overridden: %v", explicit)
	}

	withoutPKI := &inventory.Manifest{Profile: "dev", Services: map[string]inventory.ServiceConfig{
		"network-observer": {Enabled: true, Host: "core-1", Port: 28024},
	}}
	devEnv := map[string]string{}
	if err := renderBridgeNetworkObservationEnv(withoutPKI, devEnv); err != nil || devEnv["NETWORK_OBSERVER_URL"] != "http://network-observer.internal:28024" {
		t.Fatalf("development observer URL = %q (err %v), want the plain listener without an internal CA", devEnv["NETWORK_OBSERVER_URL"], err)
	}

	withoutObserver := &inventory.Manifest{Profile: "production", RootDomain: "frameworks.network", Services: map[string]inventory.ServiceConfig{"bridge": {Enabled: true}}}
	bare := map[string]string{"SYSTEM_TENANT_ID": "11111111-1111-1111-1111-111111111111"}
	if err := renderBridgeNetworkObservationEnv(withoutObserver, bare); err != nil || bare["NETWORK_OBSERVER_URL"] != "" || bare["NETWORK_PUBLIC_TENANT_IDS"] != "" {
		t.Fatalf("Bridge without an observer enabled publication: %v (err %v)", bare, err)
	}
}

// The observer has no public name: no ingress site, Navigator record or host
// proxy route. External probers reach it only through Bridge.
func TestNetworkObserverHasNoPublicIngress(t *testing.T) {
	svc := inventory.ServiceConfig{Enabled: true, Host: "core-1"}
	manifest := &inventory.Manifest{Services: map[string]inventory.ServiceConfig{"network-observer": svc}}
	if _, public := clusterderive.ManifestServiceType("network-observer", svc); public {
		t.Fatal("network-observer is a public DNS service")
	}
	if isProxyRouteService("network-observer", svc) {
		t.Fatal("network-observer is routed by the host ingress")
	}
	routes := map[string]any{}
	addLocalProxyRoutes(routes, "core-1", manifest.Services, "nginx")
	if _, ok := routes["network-observer"]; ok {
		t.Fatalf("host ingress routes = %v, want no observer route", routes)
	}
}

// A Bridge in another region reaches the observer pinned to the aggregator
// region: its URL names the aggregator-region alias, and that Bridge host's
// mesh seed resolves the alias to the observer and peers with its host.
func TestBridgeInAnotherRegionReachesObserverOverTheMesh(t *testing.T) {
	manifest := aggregatorRegionManifest()
	manifest.Services["navigator"] = inventory.ServiceConfig{Enabled: true, Host: "central-eu-1"}
	manifest.Services["network-observer"] = inventory.ServiceConfig{Enabled: true, Host: "central-eu-1"}
	env := map[string]string{}
	if err := renderBridgeNetworkObservationEnv(manifest, env); err != nil {
		t.Fatal(err)
	}
	if got := env["NETWORK_OBSERVER_URL"]; got != "https://network-observer.eu-west.internal:18034" {
		t.Fatalf("Bridge observer URL = %q, want the aggregator-region alias on the internal listener", got)
	}
	usBridge := buildPrivateerSeedDNS(manifest, "regional-us-1")
	if got := usBridge["network-observer.eu-west"]; !slices.Equal(got, []string{"10.88.0.10"}) {
		t.Fatalf("US Bridge host resolves network-observer.eu-west to %v, want the observer host", got)
	}
	if _, ok := privateerDependencyPeerHosts(manifest, "regional-us-1")["central-eu-1"]; !ok {
		t.Fatal("US Bridge host does not peer with the observer host")
	}
}

// Bridge's dependency on the observer resolves in the aggregator region, and
// the observer must register with Quartermaster so the runtime mesh DNS lists
// it too.
func TestNetworkObserverMeshURLUsesAggregatorRegionAlias(t *testing.T) {
	if scope := topology.ServiceDependencyScope("bridge", "network-observer", "NETWORK_OBSERVER_URL"); scope != topology.DNSScopeAggregatorRegion {
		t.Fatalf("Bridge to observer DNS scope = %q, want aggregator region", scope)
	}
	required := false
	for _, dep := range topology.RequiredServiceEnv("network-observer") {
		if dep.TargetServiceID == "quartermaster" && dep.EnvKey == "QUARTERMASTER_GRPC_ADDR" {
			required = true
		}
	}
	if !required {
		t.Fatal("observer registration with Quartermaster is not a required dependency")
	}
}
