package clusterderive

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestWildcardBundleDomains(t *testing.T) {
	tests := []struct {
		name       string
		rootDomain string
		want       []string
	}{
		{
			name:       "apex root",
			rootDomain: "frameworks.network",
			want:       []string{"frameworks.network", "*.frameworks.network"},
		},
		{
			name:       "cluster-scoped root",
			rootDomain: "core-central-primary.frameworks.network",
			want:       []string{"core-central-primary.frameworks.network", "*.core-central-primary.frameworks.network"},
		},
		{
			name:       "trims surrounding whitespace",
			rootDomain: "  frameworks.network  ",
			want:       []string{"frameworks.network", "*.frameworks.network"},
		},
		{
			name:       "normalizes URL input",
			rootDomain: "https://frameworks.network/",
			want:       []string{"frameworks.network", "*.frameworks.network"},
		},
		{
			name:       "empty input",
			rootDomain: "",
			want:       nil,
		},
		{
			name:       "whitespace-only input",
			rootDomain: "   ",
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WildcardBundleDomains(tt.rootDomain)
			if !slices.Equal(got, tt.want) {
				t.Errorf("WildcardBundleDomains(%q) = %v, want %v", tt.rootDomain, got, tt.want)
			}
		})
	}
}

func TestLogicalServiceClusterIDsDefaultsBunnyServicesToDefaultMediaCluster(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"central-eu-1": {Cluster: "core-central-primary"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary":  {Name: "Core Central Primary", Type: "central"},
			"media-central-primary": {Name: "Media Central Primary", Type: "edge", Default: true, Roles: []string{"media"}},
		},
	}

	got := LogicalServiceClusterIDs("foghorn", inventory.ServiceConfig{Enabled: true, Host: "central-eu-1"}, manifest)
	if !slices.Equal(got, []string{"media-central-primary"}) {
		t.Fatalf("foghorn cluster = %q, want media-central-primary", got)
	}

	got = LogicalServiceClusterIDs("chandler", inventory.ServiceConfig{Enabled: true, Host: "central-eu-1"}, manifest)
	if !slices.Equal(got, []string{"media-central-primary"}) {
		t.Fatalf("chandler cluster = %q, want media-central-primary", got)
	}
}

func TestLogicalServiceClusterIDsDefaultsTelemetryToAllMediaClusters(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {Cluster: "regional-eu"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"regional-eu": {Name: "Regional EU", Type: "regional"},
			"media-eu-1":  {Name: "Media EU 1", Type: "edge", Default: true, Roles: []string{"media"}},
			"media-us-1":  {Name: "Media US 1", Type: "edge", Roles: []string{"media"}},
		},
	}

	got := LogicalServiceClusterIDs("vmauth", inventory.ServiceConfig{Enabled: true, Host: "regional-eu-1"}, manifest)
	want := []string{"media-eu-1", "media-us-1"}
	if !slices.Equal(got, want) {
		t.Fatalf("vmauth telemetry clusters = %q, want %q", got, want)
	}
}

func TestHostScopedLogicalServiceClusterIDsScopesTelemetryByRegion(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"regional-eu-1": {Cluster: "regional-eu"},
			"regional-us-1": {Labels: map[string]string{"region": "us-east"}},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"regional-eu": {Name: "Regional EU", Type: "regional", Region: "eu-west"},
			"media-eu-1":  {Name: "Media EU 1", Type: "edge", Default: true, Roles: []string{"media"}, Region: "eu-west"},
			"media-us-1":  {Name: "Media US 1", Type: "edge", Roles: []string{"media"}, Region: "us-east"},
		},
	}

	got, ok := HostScopedLogicalServiceClusterIDs("vmauth", inventory.ServiceConfig{Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}}, manifest, "regional-eu-1")
	if !ok {
		t.Fatal("expected vmauth to resolve logical clusters")
	}
	if !slices.Equal(got, []string{"media-eu-1"}) {
		t.Fatalf("regional-eu-1 clusters = %q, want media-eu-1", got)
	}

	got, ok = HostScopedLogicalServiceClusterIDs("vmauth", inventory.ServiceConfig{Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}}, manifest, "regional-us-1")
	if !ok {
		t.Fatal("expected vmauth to resolve logical clusters")
	}
	if !slices.Equal(got, []string{"media-us-1"}) {
		t.Fatalf("regional-us-1 clusters = %q, want media-us-1", got)
	}
}

func TestLogicalServiceClusterIDsHonorsExplicitClusters(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"central-eu-1": {Cluster: "core-central-primary"},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary":  {Name: "Core Central Primary", Type: "central"},
			"media-central-primary": {Name: "Media Central Primary", Type: "edge", Default: true, Roles: []string{"media"}},
			"media-dedicated":       {Name: "Dedicated Media", Type: "edge", Roles: []string{"media"}},
		},
	}

	got := LogicalServiceClusterIDs("livepeer-gateway", inventory.ServiceConfig{Enabled: true, Host: "central-eu-1", Cluster: "media-dedicated"}, manifest)
	if !slices.Equal(got, []string{"media-dedicated"}) {
		t.Fatalf("livepeer-gateway cluster = %q, want media-dedicated", got)
	}

	got = LogicalServiceClusterIDs("livepeer-gateway", inventory.ServiceConfig{Enabled: true, Host: "central-eu-1", Clusters: []string{"media-central-primary", "media-dedicated"}}, manifest)
	if !slices.Equal(got, []string{"media-central-primary", "media-dedicated"}) {
		t.Fatalf("livepeer-gateway clusters = %q, want both explicit clusters", got)
	}
}

func TestLogicalServiceClusterIDsSupportsManifestAliases(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Type: "edge", Roles: []string{"media"}},
			"media-us-1": {Type: "edge", Roles: []string{"media"}},
		},
	}

	got := LogicalServiceClusterIDs("foghorn-us", inventory.ServiceConfig{
		Enabled: true,
		Deploy:  "foghorn",
		Host:    "regional-us-1",
		Cluster: "media-us-1",
	}, manifest)
	if !slices.Equal(got, []string{"media-us-1"}) {
		t.Fatalf("alias cluster = %q, want media-us-1", got)
	}

	serviceType, ok := ManifestServiceType("livepeer-gateway-eu", inventory.ServiceConfig{Deploy: "livepeer-gateway"})
	if !ok || serviceType != "livepeer-gateway" {
		t.Fatalf("alias service type = %q, %v; want livepeer-gateway, true", serviceType, ok)
	}
}

func TestMediaClusterIDsFiltersCoreClusters(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary":  {Type: "central", Roles: []string{"control"}},
			"media-central-primary": {Type: "edge", Roles: []string{"media"}},
			"media-secondary":       {Roles: []string{"media"}},
		},
	}

	got := MediaClusterIDs(manifest)
	want := []string{"media-central-primary", "media-secondary"}
	if !slices.Equal(got, want) {
		t.Fatalf("media clusters = %v, want %v", got, want)
	}
}

func TestPublicServiceDomainsNormalizeRootDomain(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "https://frameworks.network/",
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Name: "Media EU 1", Type: "edge"},
		},
	}

	if got := ClusterScopedRootDomain(manifest, "media-eu-1"); got != "media-eu-1.frameworks.network" {
		t.Fatalf("ClusterScopedRootDomain = %q", got)
	}
	if got := PublicServiceRootDomain("bridge", manifest, "media-eu-1"); got != "frameworks.network" {
		t.Fatalf("PublicServiceRootDomain bridge = %q", got)
	}
	domains, bundleID := AutoIngressDomainsForService("chandler", inventory.ServiceConfig{}, manifest, "media-eu-1")
	if !slices.Equal(domains, []string{"chandler.media-eu-1.frameworks.network"}) {
		t.Fatalf("AutoIngressDomainsForService domains = %v", domains)
	}
	if bundleID != "wildcard-media-eu-1-frameworks-network" {
		t.Fatalf("AutoIngressDomainsForService bundleID = %q", bundleID)
	}
}
