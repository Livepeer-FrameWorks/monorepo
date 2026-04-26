package mesh

import (
	"strings"
	"testing"
)

func TestUpdateClusterYAMLWritesNoBootstrapMode(t *testing.T) {
	in := []byte("version: v1\ntype: cluster\nhosts:\n  a:\n    external_ip: 1.2.3.4\n")
	out, err := UpdateClusterYAML(in, map[string]HostWG{
		"a": {WireguardIP: "10.88.0.2", WireguardPublicKey: "pub", WireguardPort: 51820},
	}, WireGuardBlock{Enabled: true, MeshCIDR: "10.88.0.0/16", ListenPort: 51820})
	if err != nil {
		t.Fatalf("UpdateClusterYAML: %v", err)
	}
	if strings.Contains(string(out), "bootstrap_mode") {
		t.Fatalf("cluster.yaml output still contains bootstrap_mode:\n%s", out)
	}
	for _, want := range []string{"wireguard_ip: 10.88.0.2", "mesh_cidr: 10.88.0.0/16", "listen_port: 51820"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestUpdateClusterYAMLPreservesFormatting(t *testing.T) {
	in := []byte(`version: v1
type: cluster
env_files:
  - ../../config/production.env
  - ../../secrets/production.env

hosts:
  central-eu-1:
    cluster: core-central-primary
    roles: [infrastructure, services, interfaces]
    labels:
      region: eu-west
  regional-us-1:
    cluster: core-central-primary
    roles: [services, interfaces]
    labels:
      region: us-east

services:
  # Central — control plane + support
  quartermaster:
    enabled: true
    mode: native
    host: central-eu-1

observability:
  grafana:
    enabled: true
    mode: docker
    port: 3000
`)
	out, err := UpdateClusterYAML(in, map[string]HostWG{
		"central-eu-1": {
			WireguardIP:        "10.88.156.88",
			WireguardPublicKey: "pub-central",
			WireguardPort:      51820,
		},
		"regional-us-1": {
			WireguardIP:        "10.88.236.29",
			WireguardPublicKey: "pub-us",
			WireguardPort:      51820,
		},
	}, WireGuardBlock{Enabled: true, MeshCIDR: "10.88.0.0/16", ListenPort: 51820})
	if err != nil {
		t.Fatalf("UpdateClusterYAML: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"env_files:\n  - ../../config/production.env\n  - ../../secrets/production.env\n\nhosts:",
		"services:\n  # Central — control plane + support\n  quartermaster:\n    enabled: true",
		"observability:\n  grafana:\n    enabled: true\n    mode: docker\n    port: 3000\n\nwireguard:",
		"  central-eu-1:\n    cluster: core-central-primary\n    roles: [infrastructure, services, interfaces]\n    labels:\n      region: eu-west\n    wireguard_ip: 10.88.156.88\n    wireguard_public_key: pub-central\n    wireguard_port: 51820",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output did not preserve expected fragment %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "    - ../../config") || strings.Contains(got, "        enabled: true") {
		t.Fatalf("output appears to have been globally reindented:\n%s", got)
	}
}
