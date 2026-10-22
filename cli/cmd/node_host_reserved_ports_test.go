package cmd

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

func TestNodeTuningReservesTheHostsWireGuardPort(t *testing.T) {
	stubNodeHostProvisioners(t)
	manifest := &inventory.Manifest{
		WireGuard: &inventory.WireGuardConfig{ListenPort: 51900},
		Hosts: map[string]inventory.Host{
			"own-port":     {Name: "own-port", WireguardIP: "10.0.0.1", WireguardPort: 51821},
			"cluster-port": {Name: "cluster-port", WireguardIP: "10.0.0.2"},
			"no-mesh":      {Name: "no-mesh"},
		},
	}
	for host, want := range map[string][]int{
		"own-port":     {51821},
		"cluster-port": {51900},
		"no-mesh":      nil,
	} {
		roles, err := nodeHostRoles(nil, manifest, manifest.Hosts[host], false)
		if err != nil {
			t.Fatal(err)
		}
		var tuning provisioner.ServiceConfig
		for _, role := range roles {
			if role.name == "node tuning" {
				tuning = role.config
			}
		}
		got, _ := tuning.Metadata["reserved_ports"].([]int)
		if !slices.Equal(got, want) {
			t.Errorf("%s: node tuning reserved_ports = %v, want %v", host, got, want)
		}
	}
}

// stubNodeHostProvisioners replaces the role provisioners so roles build
// without an SSH pool.
func stubNodeHostProvisioners(t *testing.T) {
	t.Helper()
	original := nodeHostProvisioners
	t.Cleanup(func() { nodeHostProvisioners = original })
	nodeHostProvisioners = func(_ *ssh.Pool) (provisioner.Provisioner, provisioner.Provisioner, error) {
		return nil, nil, nil
	}
}
