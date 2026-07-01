package inventory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestValidateClickHouseTopology(t *testing.T) {
	base := func(ch *ClickHouseConfig) *Manifest {
		return &Manifest{
			Version: "1",
			Type:    "cluster",
			Hosts: map[string]Host{
				"ch-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
				"ch-2": {ExternalIP: "10.0.0.11", User: "root", WireguardIP: "10.88.0.11"},
			},
			Infrastructure: InfrastructureConfig{ClickHouse: ch},
		}
	}
	cases := []struct {
		name    string
		ch      *ClickHouseConfig
		wantErr string // substring; "" = must pass
	}{
		{"host_tombstone_rejected", &ClickHouseConfig{Enabled: true, Host: "ch-1"}, "clickhouse.host was removed"},
		{"nodes_required", &ClickHouseConfig{Enabled: true}, "at least one entry in 'nodes'"},
		{"duplicate_id", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}, {Host: "ch-2", ID: 1}}}, "duplicate clickhouse node id"},
		{"zero_id", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 0}}}, "must be a positive integer"},
		{"duplicate_host", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}, {Host: "ch-1", ID: 2}}}, "duplicate clickhouse node host"},
		{"unknown_host", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "nope", ID: 1}}}, "not found in hosts"},
		{"valid_single_node", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}}}, ""},
		{"multi_node_refused", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}, {Host: "ch-2", ID: 2}}}, "multi-node (2 nodes) is unsupported by this release"},
		{"read_endpoint_unknown_host", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}}, ReadEndpoint: "nope"}, "read_endpoint host 'nope' not found"},
		{"write_endpoint_unknown_host", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}}, WriteEndpoint: "nope"}, "write_endpoint host 'nope' not found"},
		{"valid_endpoints_pinned_to_other_host", &ClickHouseConfig{Enabled: true, Nodes: []ClickHouseNode{{Host: "ch-1", ID: 1}}, ReadEndpoint: "ch-2", WriteEndpoint: "ch-2"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.ch).Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestManifestValidateKafkaRequiresClusterID(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled: true,
				Brokers: []KafkaBroker{{Host: "broker-1", ID: 1}},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for missing cluster_id")
	}
}

func TestManifestValidateKafkaSingleBrokerAllowed(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "test-cluster-id-12345",
				Brokers:   []KafkaBroker{{Host: "broker-1", ID: 1}},
			},
		},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("single broker should be valid: %v", err)
	}
}

func TestManifestValidateKafkaUniqueBrokerIDs(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
			"broker-2": {ExternalIP: "10.0.0.11", User: "root", WireguardIP: "10.88.0.11"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "test-cluster-id-12345",
				Brokers: []KafkaBroker{
					{Host: "broker-1", ID: 1},
					{Host: "broker-2", ID: 1},
				},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for duplicate broker IDs")
	}
}

func TestManifestValidateKafkaControllersMinThree(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-1": {ExternalIP: "10.0.0.1", User: "root", WireguardIP: "10.88.0.1"},
			"host-2": {ExternalIP: "10.0.0.2", User: "root", WireguardIP: "10.88.0.2"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "test-cluster-id",
				Brokers:   []KafkaBroker{{Host: "host-1", ID: 1}},
				Controllers: []KafkaController{
					{Host: "host-1", ID: 100, DirID: "dir-a"},
					{Host: "host-2", ID: 101, DirID: "dir-b"},
				},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for fewer than 3 controllers")
	}
}

func TestManifestValidateKafkaControllerBrokerIDConflict(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-1": {ExternalIP: "10.0.0.1", User: "root", WireguardIP: "10.88.0.1"},
			"host-2": {ExternalIP: "10.0.0.2", User: "root", WireguardIP: "10.88.0.2"},
			"host-3": {ExternalIP: "10.0.0.3", User: "root", WireguardIP: "10.88.0.3"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "test-cluster-id",
				Brokers:   []KafkaBroker{{Host: "host-1", ID: 1}},
				Controllers: []KafkaController{
					{Host: "host-1", ID: 1, DirID: "dir-a"},
					{Host: "host-2", ID: 101, DirID: "dir-b"},
					{Host: "host-3", ID: 102, DirID: "dir-c"},
				},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for controller ID conflicting with broker ID")
	}
}

func TestManifestValidateKafkaControllerDirIDRequired(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-1": {ExternalIP: "10.0.0.1", User: "root", WireguardIP: "10.88.0.1"},
			"host-2": {ExternalIP: "10.0.0.2", User: "root", WireguardIP: "10.88.0.2"},
			"host-3": {ExternalIP: "10.0.0.3", User: "root", WireguardIP: "10.88.0.3"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "test-cluster-id",
				Brokers:   []KafkaBroker{{Host: "host-1", ID: 1}},
				Controllers: []KafkaController{
					{Host: "host-1", ID: 100, DirID: "dir-a"},
					{Host: "host-2", ID: 101, DirID: ""},
					{Host: "host-3", ID: 102, DirID: "dir-c"},
				},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for missing controller dir_id")
	}
}

func TestManifestValidateKafkaControllerHostExists(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"host-1": {ExternalIP: "10.0.0.1", User: "root", WireguardIP: "10.88.0.1"},
			"host-2": {ExternalIP: "10.0.0.2", User: "root", WireguardIP: "10.88.0.2"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "test-cluster-id",
				Brokers:   []KafkaBroker{{Host: "host-1", ID: 1}},
				Controllers: []KafkaController{
					{Host: "host-1", ID: 100, DirID: "dir-a"},
					{Host: "host-2", ID: 101, DirID: "dir-b"},
					{Host: "nonexistent", ID: 102, DirID: "dir-c"},
				},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for controller host not in hosts map")
	}
}

func TestManifestValidateAllowsServiceAliasWithKnownDeploy(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"regional-us-1": {ExternalIP: "10.0.0.1", User: "root", WireguardIP: "10.88.0.1"},
		},
		Services: map[string]ServiceConfig{
			"chandler-us": {
				Enabled: true,
				Deploy:  "chandler",
				Host:    "regional-us-1",
			},
		},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("service alias with known deploy should validate: %v", err)
	}
}

func TestMergeHostInventory(t *testing.T) {
	manifest := &Manifest{
		Hosts: map[string]Host{
			"node-1": {Cluster: "prod", Roles: []string{"infrastructure"}},
			"node-2": {Cluster: "prod", Roles: []string{"services"}},
		},
	}

	inv := &HostInventory{
		Hosts: map[string]HostConnection{
			"node-1": {ExternalIP: "10.0.0.1", User: "admin", WireguardPrivateKey: "AAAA"},
			"node-2": {ExternalIP: "10.0.0.2", User: "deploy", WireguardPrivateKey: "BBBB"},
		},
	}

	if err := manifest.MergeHostInventory(inv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if manifest.Hosts["node-1"].Name != "node-1" {
		t.Errorf("node-1 Name = %q, want %q", manifest.Hosts["node-1"].Name, "node-1")
	}
	if manifest.Hosts["node-1"].ExternalIP != "10.0.0.1" {
		t.Errorf("node-1 ExternalIP = %q, want %q", manifest.Hosts["node-1"].ExternalIP, "10.0.0.1")
	}
	if manifest.Hosts["node-1"].User != "admin" {
		t.Errorf("node-1 User = %q, want %q", manifest.Hosts["node-1"].User, "admin")
	}
	if manifest.Hosts["node-2"].User != "deploy" {
		t.Errorf("node-2 User = %q, want %q", manifest.Hosts["node-2"].User, "deploy")
	}
}

func TestMergeHostInventoryMissingHost(t *testing.T) {
	manifest := &Manifest{
		Hosts: map[string]Host{
			"node-1": {},
			"node-2": {},
		},
	}

	inv := &HostInventory{
		Hosts: map[string]HostConnection{
			"node-1": {ExternalIP: "10.0.0.1"},
		},
	}

	err := manifest.MergeHostInventory(inv)
	if err == nil {
		t.Fatal("expected error for missing host in inventory")
	}
}

func TestMergeEdgeHosts(t *testing.T) {
	manifest := &EdgeManifest{
		Nodes: []EdgeNode{
			{Name: "edge-eu-1", Subdomain: "edge-eu-1"},
			{Name: "edge-us-1", Subdomain: "edge-us-1"},
		},
	}

	inv := &HostInventory{
		EdgeNodes: map[string]EdgeConnection{
			"edge-eu-1": {ExternalIP: "1.2.3.4", User: "deploy"},
			"edge-us-1": {ExternalIP: "5.6.7.8"}, // user defaults to root
		},
	}

	if err := manifest.MergeEdgeHosts(inv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if manifest.Nodes[0].SSH != "deploy@1.2.3.4" {
		t.Errorf("edge SSH = %q, want %q", manifest.Nodes[0].SSH, "deploy@1.2.3.4")
	}
	if manifest.Nodes[0].ExternalIP != "1.2.3.4" {
		t.Errorf("edge ExternalIP = %q, want %q", manifest.Nodes[0].ExternalIP, "1.2.3.4")
	}
	if manifest.Nodes[1].SSH != "root@5.6.7.8" {
		t.Errorf("edge SSH = %q, want %q (default user=root)", manifest.Nodes[1].SSH, "root@5.6.7.8")
	}
	if manifest.Nodes[1].ExternalIP != "5.6.7.8" {
		t.Errorf("edge ExternalIP = %q, want %q", manifest.Nodes[1].ExternalIP, "5.6.7.8")
	}
}

func TestMergeEdgeHostsRejectsEmptyExternalIP(t *testing.T) {
	manifest := &EdgeManifest{
		Nodes: []EdgeNode{{Name: "edge-eu-1"}},
	}
	inv := &HostInventory{
		EdgeNodes: map[string]EdgeConnection{
			"edge-eu-1": {User: "root"}, // no external_ip
		},
	}
	err := manifest.MergeEdgeHosts(inv)
	if err == nil {
		t.Fatal("expected error for empty external_ip")
	}
	if !strings.Contains(err.Error(), "external_ip required") {
		t.Errorf("error = %q, want it to mention 'external_ip required'", err.Error())
	}
}

func TestLoadEdgeWithHostsAcceptsTypeField(t *testing.T) {
	dir := t.TempDir()

	hostsYAML := `edge_nodes:
  edge-eu-1:
    external_ip: edge-eu-1.example.com
    user: root
`
	if err := os.WriteFile(filepath.Join(dir, "hosts.yaml"), []byte(hostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestYAML := `version: v1
type: edge
root_domain: frameworks.network
pool_domain: edge.frameworks.network
email: ops@frameworks.network
cluster_id: media-central-primary
hosts_file: hosts.yaml
nodes:
  - name: edge-eu-1
    subdomain: edge-eu-1
    region: eu-west
    register_qm: true
`
	manifestPath := filepath.Join(dir, "edge.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadEdgeWithHosts(manifestPath, "")
	if err != nil {
		t.Fatalf("LoadEdgeWithHosts rejected type field: %v", err)
	}
	if manifest.Type != "edge" {
		t.Fatalf("Type = %q, want edge", manifest.Type)
	}
	if manifest.Nodes[0].SSH != "root@edge-eu-1.example.com" {
		t.Fatalf("SSH = %q, want host inventory value", manifest.Nodes[0].SSH)
	}
	if manifest.Nodes[0].ExternalIP != "edge-eu-1.example.com" {
		t.Fatalf("ExternalIP = %q, want host inventory value", manifest.Nodes[0].ExternalIP)
	}
}

// Regression: an explicitly-provided hostsPath must be used verbatim, not
// re-joined to the manifest's directory. Callers like `mesh wg generate` resolve
// the manifest's hosts_file relative to the manifest dir and pass that resolved
// path in; re-joining it produced clusters/production/clusters/production/hosts.yaml
// and broke `frameworks mesh wg generate --manifest clusters/production/cluster.yaml`.
func TestLoadWithHostsFileDoesNotDoubleJoinExplicitPath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "clusters", "production")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestYAML := `version: v1
type: cluster
hosts_file: hosts.yaml
hosts:
  ch-1:
    cluster: c
    roles: [infrastructure]
`
	hostsYAML := `hosts:
  ch-1:
    external_ip: 203.0.113.5
    user: root
`
	if err := os.WriteFile(filepath.Join(sub, "cluster.yaml"), []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "hosts.yaml"), []byte(hostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	// Run from the repo root so the relative manifest path carries a directory
	// component — the exact shape that triggered the double-join.
	t.Chdir(root)

	// The explicit hostsPath is already resolved relative to the manifest dir,
	// mirroring resolveMeshHostsFile's output.
	m, err := LoadWithHostsFileNoValidate(
		"clusters/production/cluster.yaml",
		"clusters/production/hosts.yaml",
		"",
	)
	if err != nil {
		t.Fatalf("LoadWithHostsFileNoValidate: %v", err)
	}
	h, ok := m.Hosts["ch-1"]
	if !ok {
		t.Fatalf("host ch-1 not merged from inventory: %+v", m.Hosts)
	}
	if h.ExternalIP != "203.0.113.5" || h.User != "root" {
		t.Fatalf("host inventory not merged verbatim: %+v", h)
	}
}

func TestLoadEdgeWithHostsAcceptsCapacityControls(t *testing.T) {
	dir := t.TempDir()

	hostsYAML := `edge_nodes:
  edge-eu-1:
    external_ip: edge-eu-1.example.com
    user: root
`
	if err := os.WriteFile(filepath.Join(dir, "hosts.yaml"), []byte(hostsYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestYAML := `version: v1
type: edge
root_domain: frameworks.network
pool_domain: edge.frameworks.network
email: ops@frameworks.network
hosts_file: hosts.yaml
capabilities: [edge, storage]
bandwidth_mbps: 2000
max_transcodes: 4
storage_capacity_bytes: 500000000000
nodes:
  - name: edge-eu-1
    subdomain: edge-eu-1
    capabilities: [edge]
    bandwidth_mbps: 1000
`
	manifestPath := filepath.Join(dir, "edge.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadEdgeWithHosts(manifestPath, "")
	if err != nil {
		t.Fatalf("LoadEdgeWithHosts returned error: %v", err)
	}
	if got := manifest.Nodes[0].ResolvedBandwidthMbps(manifest.BandwidthMbps); got != 1000 {
		t.Fatalf("resolved bandwidth = %d, want 1000", got)
	}
	if got := strings.Join(manifest.Nodes[0].ResolvedCapabilities(manifest.Capabilities), ","); got != "edge" {
		t.Fatalf("resolved capabilities = %q, want edge", got)
	}
	if got := manifest.Nodes[0].ResolvedMaxTranscodes(manifest.MaxTranscodes); got != 4 {
		t.Fatalf("resolved max transcodes = %d, want 4", got)
	}
	if got := manifest.Nodes[0].ResolvedStorageBytes(manifest.StorageBytes); got != 500000000000 {
		t.Fatalf("resolved storage bytes = %d, want 500000000000", got)
	}
}

func TestLoadWithHostsFile(t *testing.T) {
	dir := t.TempDir()

	hostsYAML := `hosts:
  server-1:
    external_ip: "10.0.0.1"
    user: root
    wireguard_private_key: "AAAA"
  server-2:
    external_ip: "10.0.0.2"
    user: deploy
    wireguard_private_key: "BBBB"
`
	if err := os.WriteFile(filepath.Join(dir, "hosts.yaml"), []byte(hostsYAML), 0644); err != nil {
		t.Fatal(err)
	}

	manifestYAML := `version: v1
type: cluster
hosts_file: hosts.yaml
hosts:
  server-1:
    cluster: prod
    roles: [infrastructure]
    wireguard_ip: 10.88.0.1
    wireguard_public_key: AAAA
  server-2:
    cluster: prod
    roles: [services]
    wireguard_ip: 10.88.0.2
    wireguard_public_key: BBBB
`
	manifestPath := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadWithHosts(manifestPath, "")
	if err != nil {
		t.Fatalf("LoadWithHosts failed: %v", err)
	}

	if manifest.Hosts["server-1"].ExternalIP != "10.0.0.1" {
		t.Errorf("server-1 ExternalIP = %q, want %q", manifest.Hosts["server-1"].ExternalIP, "10.0.0.1")
	}
	if manifest.Hosts["server-2"].User != "deploy" {
		t.Errorf("server-2 User = %q, want %q", manifest.Hosts["server-2"].User, "deploy")
	}
}

func TestLoadInlineIPsBackwardCompat(t *testing.T) {
	dir := t.TempDir()

	manifestYAML := `version: v1
type: cluster
hosts:
  server-1:
    external_ip: "10.0.0.1"
    user: root
    wireguard_ip: 10.88.0.1
    wireguard_public_key: AAAA
`
	manifestPath := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadWithHosts(manifestPath, "")
	if err != nil {
		t.Fatalf("LoadWithHosts failed for inline IPs: %v", err)
	}

	if manifest.Hosts["server-1"].ExternalIP != "10.0.0.1" {
		t.Errorf("server-1 ExternalIP = %q, want %q", manifest.Hosts["server-1"].ExternalIP, "10.0.0.1")
	}
}

func TestParseManifestNoValidation(t *testing.T) {
	data := []byte(`version: v1
type: cluster
hosts_file: hosts.enc.yaml
hosts:
  node-1:
    roles: [infrastructure]
`)

	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}
	if manifest.HostsFile != "hosts.enc.yaml" {
		t.Errorf("HostsFile = %q, want %q", manifest.HostsFile, "hosts.enc.yaml")
	}
	if manifest.Hosts["node-1"].ExternalIP != "" {
		t.Error("expected empty ExternalIP before merge")
	}
}

// TestLoadHostInventory_AcceptsAdoptedLocalFields confirms the encrypted
// inventory parser (strictUnmarshal with KnownFields(true)) accepts the
// adopted_local markers that `mesh reconcile` writes. Previously the
// parser rejected any file containing these fields.
func TestLoadHostInventory_AcceptsAdoptedLocalFields(t *testing.T) {
	data := []byte(`hosts:
  core-4:
    external_ip: 203.0.113.10
    user: deploy
    wireguard_private_key_file: /etc/privateer/wg.key
    wireguard_private_key_managed: false
`)
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.enc.yaml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := LoadHostInventory(path, "")
	if err != nil {
		t.Fatalf("LoadHostInventory rejected adopted_local fields: %v", err)
	}
	h, ok := inv.Hosts["core-4"]
	if !ok {
		t.Fatal("core-4 missing from inventory")
	}
	if h.WireguardPrivateKeyFile != "/etc/privateer/wg.key" {
		t.Errorf("WireguardPrivateKeyFile = %q", h.WireguardPrivateKeyFile)
	}
	if h.WireguardPrivateKeyManaged == nil || *h.WireguardPrivateKeyManaged {
		t.Errorf("WireguardPrivateKeyManaged should be non-nil false, got %+v", h.WireguardPrivateKeyManaged)
	}
}

// TestMergeHostInventory_CopiesAdoptedLocalFields confirms the adopted_local
// fields propagate from HostConnection onto the manifest's Host so the
// Ansible provisioner metadata picks them up.
func TestMergeHostInventory_CopiesAdoptedLocalFields(t *testing.T) {
	m := &Manifest{
		Hosts: map[string]Host{
			"core-4": {},
		},
	}
	managedFalse := false
	inv := &HostInventory{
		Hosts: map[string]HostConnection{
			"core-4": {
				ExternalIP:                 "203.0.113.10",
				User:                       "deploy",
				WireguardPrivateKeyFile:    "/etc/privateer/wg.key",
				WireguardPrivateKeyManaged: &managedFalse,
			},
		},
	}
	if err := m.MergeHostInventory(inv); err != nil {
		t.Fatalf("MergeHostInventory: %v", err)
	}
	h := m.Hosts["core-4"]
	if h.WireguardPrivateKeyFile != "/etc/privateer/wg.key" {
		t.Errorf("WireguardPrivateKeyFile not merged: %+v", h)
	}
	if h.WireguardPrivateKeyManaged == nil || *h.WireguardPrivateKeyManaged {
		t.Errorf("WireguardPrivateKeyManaged not merged: %+v", h.WireguardPrivateKeyManaged)
	}
}

// TestManifestValidateKafkaTopLevelDefaultsToAggregator confirms the
// back-compat path: an existing manifest with no top-level role and regional
// entries declared today still parses, with the top-level treated as the
// aggregator.
func TestManifestValidateKafkaTopLevelDefaultsToAggregator(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"eu-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
			"us-1": {ExternalIP: "10.0.1.10", User: "root", WireguardIP: "10.88.1.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-cluster",
				Brokers:   []KafkaBroker{{Host: "eu-1", ID: 1}},
				Regional: []RegionalKafkaCluster{
					{RegionID: "us-east", ClusterID: "us-cluster", Brokers: []KafkaBroker{{Host: "us-1", ID: 11}}},
				},
			},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("legacy shape (no top-level role) should validate as aggregator: %v", err)
	}
}

func TestManifestValidateKafkaRejectsTwoAggregators(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"eu-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
			"us-1": {ExternalIP: "10.0.1.10", User: "root", WireguardIP: "10.88.1.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-cluster",
				Role:      "aggregator",
				Brokers:   []KafkaBroker{{Host: "eu-1", ID: 1}},
				Regional: []RegionalKafkaCluster{
					{RegionID: "us-east", Role: "aggregator", ClusterID: "us-cluster", Brokers: []KafkaBroker{{Host: "us-1", ID: 11}}},
				},
			},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("two aggregators must be rejected")
	}
}

func TestManifestValidateKafkaRejectsUnknownRole(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"eu-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-cluster",
				Role:      "lol",
				Brokers:   []KafkaBroker{{Host: "eu-1", ID: 1}},
			},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("unknown role must be rejected")
	}
}

func TestManifestValidateKafkaRejectsDuplicateRegionID(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"eu-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
			"us-1": {ExternalIP: "10.0.1.10", User: "root", WireguardIP: "10.88.1.10"},
			"us-2": {ExternalIP: "10.0.1.11", User: "root", WireguardIP: "10.88.1.11"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-cluster",
				Brokers:   []KafkaBroker{{Host: "eu-1", ID: 1}},
				Regional: []RegionalKafkaCluster{
					{RegionID: "us-east", ClusterID: "us-cluster-1", Brokers: []KafkaBroker{{Host: "us-1", ID: 11}}},
					{RegionID: "us-east", ClusterID: "us-cluster-2", Brokers: []KafkaBroker{{Host: "us-2", ID: 12}}},
				},
			},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("duplicate region_id must be rejected")
	}
}

func TestManifestValidateKafkaRejectsRegionalWithoutRegionID(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"eu-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
			"us-1": {ExternalIP: "10.0.1.10", User: "root", WireguardIP: "10.88.1.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-cluster",
				Brokers:   []KafkaBroker{{Host: "eu-1", ID: 1}},
				Regional: []RegionalKafkaCluster{
					{ClusterID: "us-cluster", Brokers: []KafkaBroker{{Host: "us-1", ID: 11}}},
				},
			},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("regional entry without region_id must be rejected")
	}
}

func TestManifestValidateKafkaAcceptsThreeRegionTopology(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"eu-1": {ExternalIP: "10.0.0.10", User: "root", WireguardIP: "10.88.0.10"},
			"us-1": {ExternalIP: "10.0.1.10", User: "root", WireguardIP: "10.88.1.10"},
			"ap-1": {ExternalIP: "10.0.2.10", User: "root", WireguardIP: "10.88.2.10"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled:   true,
				ClusterID: "eu-cluster",
				RegionID:  "eu-west",
				Role:      "aggregator",
				Brokers:   []KafkaBroker{{Host: "eu-1", ID: 1}},
				Regional: []RegionalKafkaCluster{
					{RegionID: "us-east", Role: "regional", ClusterID: "us-cluster", Brokers: []KafkaBroker{{Host: "us-1", ID: 11}}},
					{RegionID: "ap-south", Role: "regional", ClusterID: "ap-cluster", Brokers: []KafkaBroker{{Host: "ap-1", ID: 21}}},
				},
			},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("explicit 3-region topology should validate: %v", err)
	}
}
