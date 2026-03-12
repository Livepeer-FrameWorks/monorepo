package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestValidateKafkaRequiresZookeeperConnect(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {ExternalIP: "10.0.0.10", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled: true,
				Brokers: []KafkaBroker{{Host: "broker-1", ID: 1}},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for missing zookeeper_connect")
	}
}

func TestManifestValidateKafkaWithZookeeperEnsemble(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {ExternalIP: "10.0.0.10", User: "root"},
			"zk-1":     {ExternalIP: "10.0.0.20", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled: true,
				Brokers: []KafkaBroker{{Host: "broker-1", ID: 1}},
			},
			Zookeeper: &ZookeeperConfig{
				Enabled:  true,
				Ensemble: []ZookeeperNode{{Host: "zk-1", ID: 1}},
			},
		},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
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
			"node-1": {ExternalIP: "10.0.0.1", User: "admin"},
			"node-2": {ExternalIP: "10.0.0.2", User: "deploy", SSHKey: "/keys/id"},
		},
	}

	if err := manifest.MergeHostInventory(inv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if manifest.Hosts["node-1"].ExternalIP != "10.0.0.1" {
		t.Errorf("node-1 ExternalIP = %q, want %q", manifest.Hosts["node-1"].ExternalIP, "10.0.0.1")
	}
	if manifest.Hosts["node-1"].User != "admin" {
		t.Errorf("node-1 User = %q, want %q", manifest.Hosts["node-1"].User, "admin")
	}
	if manifest.Hosts["node-2"].SSHKey != "/keys/id" {
		t.Errorf("node-2 SSHKey = %q, want %q", manifest.Hosts["node-2"].SSHKey, "/keys/id")
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
  server-2:
    external_ip: "10.0.0.2"
    user: deploy
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
  server-2:
    cluster: prod
    roles: [services]
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
