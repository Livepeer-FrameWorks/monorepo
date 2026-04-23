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
