package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

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
		},
	}

	inv := &HostInventory{
		EdgeNodes: map[string]EdgeConnection{
			"edge-eu-1": {SSH: "root@1.2.3.4"},
		},
	}

	if err := manifest.MergeEdgeHosts(inv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if manifest.Nodes[0].SSH != "root@1.2.3.4" {
		t.Errorf("edge SSH = %q, want %q", manifest.Nodes[0].SSH, "root@1.2.3.4")
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
