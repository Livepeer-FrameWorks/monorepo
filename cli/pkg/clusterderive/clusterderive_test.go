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
