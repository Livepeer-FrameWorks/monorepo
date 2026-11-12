package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

func TestTenantAliasIngressRecognizesDeployedFoghornNames(t *testing.T) {
	for _, mode := range []string{"native", "docker"} {
		t.Run(mode, func(t *testing.T) {
			manifest := &inventory.Manifest{Profile: "production", RootDomain: "frameworks.network", Hosts: map[string]inventory.Host{"us-1": {ExternalIP: "192.0.2.1", Cluster: "regional-us", WireguardIP: "10.88.0.1"}}, Clusters: map[string]inventory.ClusterConfig{"regional-us": {}, "media-us-1": {}}, Services: map[string]inventory.ServiceConfig{"foghorn-us": {Enabled: true, Deploy: "foghorn", Host: "us-1", Cluster: "media-us-1", Port: 18008, Mode: mode}}, Interfaces: map[string]inventory.ServiceConfig{"nginx": {Enabled: true, Host: "us-1", Mode: "native"}}}
			cfg, err := buildTaskConfig(&orchestrator.Task{Name: "nginx", Type: "nginx", ServiceID: "nginx", Host: "us-1", ClusterID: "regional-us", Phase: orchestrator.PhaseInterfaces}, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			tap, ok := cfg.Metadata["tenant_alias_playback"].(map[string]any)
			if !ok {
				t.Fatal("generic alias TLS site missing for deployed foghorn-us")
			}
			if tap["upstream_port"] != 18008 || tap["mesh_only"] != true {
				t.Fatalf("alias upstream differs from local Foghorn: %+v", tap)
			}
		})
	}
}
