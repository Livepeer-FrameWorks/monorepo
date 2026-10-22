package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func stackTargetTestManifest() (*inventory.Manifest, *inventory.EdgeManifest) {
	m := &inventory.Manifest{
		Type:       "cluster",
		RootDomain: "staging.example.net",
		Hosts: map[string]inventory.Host{
			"core": {ExternalIP: "10.0.0.16", User: "ops"},
			"eu-1": {ExternalIP: "10.0.0.17", User: "ops"},
			"us-1": {ExternalIP: "10.0.0.20", User: "ops"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central": {Type: "central", Roles: []string{"control"}},
			"media-us":     {Type: "edge", Roles: []string{"media"}},
			"media-eu":     {Type: "edge", Roles: []string{"media"}, Default: true},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "yugabyte",
				Instances: []inventory.PostgresInstance{{Name: "local", Host: "core"}}},
		},
		Services: map[string]inventory.ServiceConfig{
			"commodore":       {Enabled: true, Host: "core"},
			"purser":          {Enabled: true, Host: "core"},
			"periscope-query": {Enabled: true, Host: "core"},
			"foghorn-eu":      {Enabled: true, Deploy: "foghorn", Hosts: []string{"eu-1"}, Cluster: "media-eu"},
			"foghorn-us":      {Enabled: true, Deploy: "foghorn", Hosts: []string{"us-1"}, Cluster: "media-us"},
			"skipper":         {Enabled: false, Host: "core"},
		},
	}
	edges := &inventory.EdgeManifest{
		RootDomain: "staging.example.net",
		ClusterID:  "media-eu",
		Nodes: []inventory.EdgeNode{
			{Name: "edge-us-node", Subdomain: "edge-us", Cluster: "media-us", SSH: "ops@10.0.0.24"},
			{Name: "edge-eu-node", Subdomain: "edge-eu", SSH: "ops@10.0.0.23"},
		},
	}
	return m, edges
}

func stackTargetEnvMap(t *testing.T, target *stackTarget) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, kv := range target.env {
		if _, dup := out[kv[0]]; dup {
			t.Fatalf("%s emitted twice", kv[0])
		}
		out[kv[0]] = kv[1]
	}
	return out
}

func TestStackTargetDerivesEndpointsFromManifest(t *testing.T) {
	m, edges := stackTargetTestManifest()
	target, err := buildStackTarget(m, edges)
	if err != nil {
		t.Fatalf("buildStackTarget: %v", err)
	}
	env := stackTargetEnvMap(t, target)
	want := map[string]string{
		"BRIDGE_URL":                   "https://bridge.staging.example.net",
		"BRIDGE_WS_URL":                "wss://bridge.staging.example.net/graphql/ws",
		"CELL_A_CLUSTER":               "media-eu", // the default media cluster is cell A
		"CELL_B_CLUSTER":               "media-us",
		"FOGHORN_A_URLS":               "https://foghorn.media-eu.staging.example.net",
		"FOGHORN_B_URL":                "https://foghorn.media-us.staging.example.net",
		"EDGE_A_RTMP":                  "rtmp://edge-eu.media-eu.staging.example.net:1935/live",
		"EDGE_B_HOST":                  "edge-us.media-us.staging.example.net",
		"STACK_EDGE_A_LOCATION_PREFIX": "https://edge-eu.media-eu.staging.example.net/",
		"STACK_EDGE_A_SERVICE":         "edge",
		"STACK_EDGE_B_SERVICE":         "edge-b",
		"FOGHORN_A_DB":                 "foghorn_a",
		"FOGHORN_B_DB":                 "foghorn_b",
		"STACK_EDGE_STORAGE_DIR":       edgeNativeStorageDir,
		"CHANDLER_URL":                 "https://chandler.media-eu.staging.example.net",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
}

func TestStackTargetServiceMapNamesEveryReplicaUnit(t *testing.T) {
	m, edges := stackTargetTestManifest()
	target, err := buildStackTarget(m, edges)
	if err != nil {
		t.Fatalf("buildStackTarget: %v", err)
	}
	for _, row := range []string{
		"commodore|ops@10.0.0.16|frameworks-commodore.service",
		"foghorn-eu|ops@10.0.0.17|frameworks-foghorn.service",
		"edge|ops@10.0.0.23|frameworks-helmsman.service",
		"edge|ops@10.0.0.23|frameworks-mistserver.service",
		"edge-b|ops@10.0.0.24|frameworks-caddy.service",
	} {
		if !slices.Contains(target.services, row) {
			t.Errorf("service map lacks %q; rows: %v", row, target.services)
		}
	}
	for _, row := range target.services {
		if strings.HasPrefix(row, "skipper|") {
			t.Errorf("disabled service mapped: %q", row)
		}
	}
}

func TestStackTargetAccessMapKeepsCredentialsOnHosts(t *testing.T) {
	m, edges := stackTargetTestManifest()
	target, err := buildStackTarget(m, edges)
	if err != nil {
		t.Fatalf("buildStackTarget: %v", err)
	}
	for _, row := range []string{
		"pg|foghorn_a|ops@10.0.0.17|/etc/frameworks/foghorn.env|ops@10.0.0.16",
		"pg|foghorn_b|ops@10.0.0.20|/etc/frameworks/foghorn.env|ops@10.0.0.16",
		"pg|commodore|ops@10.0.0.16|/etc/frameworks/commodore.env|ops@10.0.0.16",
		"ch|clickhouse|ops@10.0.0.16|/etc/frameworks/periscope-query.env|8123",
	} {
		if !slices.Contains(target.access, row) {
			t.Errorf("access map lacks %q; rows: %v", row, target.access)
		}
	}
}

func TestStackTargetRefusesOneMediaCell(t *testing.T) {
	m, edges := stackTargetTestManifest()
	delete(m.Clusters, "media-us")
	if _, err := buildStackTarget(m, edges); err == nil || !strings.Contains(err.Error(), "two media clusters") {
		t.Fatalf("err = %v, want a two-media-clusters refusal", err)
	}
}

func TestStackTargetOperatorFromBootstrapOverlay(t *testing.T) {
	dir := t.TempDir()
	overlay := `accounts:
  - kind: system_operator
    tenant:
      ref: quartermaster.system_tenant
    users:
      - email: viewer@example.net
        role: member
      - email: operator@example.net
        role: owner
        platform_operator: true
        password_ref:
          env: STACK_TARGET_TEST_PASSWORD
`
	if err := os.WriteFile(filepath.Join(dir, "bootstrap.yaml"), []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STACK_TARGET_TEST_PASSWORD", "s3cret")
	email, password, err := stackTargetOperator(&inventory.Manifest{BootstrapOverlay: "bootstrap.yaml"}, dir, "")
	if err != nil {
		t.Fatalf("stackTargetOperator: %v", err)
	}
	if email != "operator@example.net" || password != "s3cret" {
		t.Fatalf("operator = %q/%q, want operator@example.net/s3cret", email, password)
	}
}

func TestRenderStackTargetEnvQuotesForShell(t *testing.T) {
	got := renderStackTargetEnv([][2]string{{"A", "it's"}})
	if got != `A='it'\''s'`+"\n" {
		t.Fatalf("rendered %q", got)
	}
}
