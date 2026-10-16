package cmd

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// Service listeners bind loopback and the host's WireGuard address, so hosts
// reach each other only over the mesh. A multi-host manifest without
// Privateer would deploy services nobody on another host can dial.
func TestValidateProvisionMeshIdentityRefusesMultiHostWithoutMesh(t *testing.T) {
	multi := &inventory.Manifest{Hosts: map[string]inventory.Host{
		"central-eu-1":  {ExternalIP: "203.0.113.1"},
		"regional-eu-1": {ExternalIP: "203.0.113.2"},
	}}
	err := validateProvisionMeshIdentity(multi, "Run: frameworks mesh wg generate")
	if err == nil || !strings.Contains(err.Error(), "needs the WireGuard mesh") {
		t.Fatalf("multi-host manifest without privateer: err = %v, want a refusal naming the mesh", err)
	}
	single := &inventory.Manifest{Hosts: map[string]inventory.Host{"central-eu-1": {ExternalIP: "203.0.113.1"}}}
	if err := validateProvisionMeshIdentity(single, "Run: frameworks mesh wg generate"); err != nil {
		t.Fatalf("single-host manifest without privateer: %v", err)
	}
}

// The telemetry route fronts vmauth, which listens on loopback and the
// WireGuard address only; a docker-mode proxy must reach it on the latter.
func TestBuildProxySitesCarriesMeshOnlyExtraRoutes(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Hosts:      map[string]inventory.Host{"regional-eu-1": {ExternalIP: "203.0.113.2", WireguardIP: "10.88.0.2", Cluster: "media-eu"}},
		Clusters:   map[string]inventory.ClusterConfig{"media-eu": {}},
	}
	routes := []map[string]any{{
		"name":         "telemetry-media-eu",
		"server_names": []string{"telemetry.media-eu.frameworks.network"},
		"upstream":     "127.0.0.1:8427",
		"mesh_only":    true,
	}}
	sites := buildProxySitesForHost(manifest, "regional-eu-1", "media-eu", map[string]any{}, routes)
	for _, site := range sites {
		if site["name"] == "telemetry-media-eu" {
			if meshOnly, _ := site["mesh_only"].(bool); !meshOnly {
				t.Fatalf("telemetry site lost mesh_only: %#v", site)
			}
			return
		}
	}
	t.Fatalf("telemetry site missing: %#v", sites)
}

// A signer's metrics are on its http_addr, which binds the WireGuard address
// alone, so the local vmagent scrapes it there.
func TestBuildVMAgentScrapeTargetsScrapesSignerOnItsHTTPAddr(t *testing.T) {
	manifest := &inventory.Manifest{
		Profile: "production",
		Hosts:   map[string]inventory.Host{"central-eu-1": {ExternalIP: "203.0.113.1", WireguardIP: "10.88.0.1"}},
		Services: map[string]inventory.ServiceConfig{
			"livepeer-signer": {Enabled: true, Host: "central-eu-1", Port: 18016, Config: map[string]string{"http_addr": "10.88.0.1:18016"}},
		},
	}
	findScrapeTarget(t, buildVMAgentScrapeTargets(manifest, "central-eu-1"), "livepeer-signer", "10.88.0.1:18016")
}
