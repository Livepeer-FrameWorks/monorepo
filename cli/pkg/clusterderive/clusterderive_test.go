package clusterderive

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
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

// Intent: SelfRegisters must be exactly {bridge,foghorn,chandler}. These
// services create their own service_registry rows at startup via
// Quartermaster.BootstrapService, so bootstrap must NOT pre-register them or
// the runtime BootstrapService call collides with a bootstrap-seeded row.
func TestSelfRegisters(t *testing.T) {
	selfRegistering := map[string]bool{"bridge": true, "foghorn": true, "chandler": true}
	for _, name := range []string{"bridge", "foghorn", "chandler", "commodore", "quartermaster", "navigator", "livepeer-gateway", "", "edge"} {
		want := selfRegistering[name]
		if got := SelfRegisters(name); got != want {
			t.Fatalf("SelfRegisters(%q) = %v, want %v", name, got, want)
		}
	}
}

// Intent: IsPlatformOfficialCluster is true when the cluster carries the
// platform_official flag OR the equivalent Class string ("platform_official"),
// and false for missing clusters, tenant-private clusters, or a nil manifest.
func TestIsPlatformOfficialCluster(t *testing.T) {
	manifest := &inventory.Manifest{
		Clusters: map[string]inventory.ClusterConfig{
			"by-flag":  {PlatformOfficial: true},
			"by-class": {Class: "platform_official"},
			"private":  {Class: "tenant_private"},
			"neither":  {},
		},
	}
	cases := []struct {
		name      string
		manifest  *inventory.Manifest
		clusterID string
		want      bool
	}{
		{"flag set", manifest, "by-flag", true},
		{"class string", manifest, "by-class", true},
		{"tenant private", manifest, "private", false},
		{"neither flag nor class", manifest, "neither", false},
		{"missing cluster", manifest, "ghost", false},
		{"empty id", manifest, "", false},
		{"nil manifest", nil, "by-flag", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPlatformOfficialCluster(tc.manifest, tc.clusterID); got != tc.want {
				t.Fatalf("IsPlatformOfficialCluster(_, %q) = %v, want %v", tc.clusterID, got, tc.want)
			}
		})
	}
}

// Intent: AutoIngressDomains is the no-svc-alias wrapper around
// AutoIngressDomainsForService — same result for a service with no manifest
// alias. Pin the foredeck apex case and the unknown-service empty case.
func TestAutoIngressDomains(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"media-eu-1": {Name: "Media EU 1", Type: "edge"},
		},
	}

	t.Run("foredeck apex", func(t *testing.T) {
		domains, bundleID := AutoIngressDomains("foredeck", manifest, "media-eu-1")
		want := []string{"frameworks.network", "www.frameworks.network"}
		if !slices.Equal(domains, want) {
			t.Fatalf("foredeck domains = %v, want %v", domains, want)
		}
		if bundleID != TLSBundleID("apex", manifest.RootDomain) {
			t.Fatalf("foredeck bundleID = %q, want %q", bundleID, TLSBundleID("apex", manifest.RootDomain))
		}
	})

	t.Run("matches AutoIngressDomainsForService with empty svc", func(t *testing.T) {
		d1, b1 := AutoIngressDomains("chandler", manifest, "media-eu-1")
		d2, b2 := AutoIngressDomainsForService("chandler", inventory.ServiceConfig{}, manifest, "media-eu-1")
		if !slices.Equal(d1, d2) || b1 != b2 {
			t.Fatalf("AutoIngressDomains wrapper diverges from AutoIngressDomainsForService: (%v,%q) vs (%v,%q)", d1, b1, d2, b2)
		}
	})

	t.Run("unknown service", func(t *testing.T) {
		domains, bundleID := AutoIngressDomains("does-not-exist", manifest, "media-eu-1")
		if domains != nil || bundleID != "" {
			t.Fatalf("unknown service = (%v,%q), want (nil,\"\")", domains, bundleID)
		}
	})
}

// Intent: PlatformGlobalRootIngressDomainsForService emits the global media
// root FQDN (e.g. foghorn.frameworks.network) only for platform pool services
// (foghorn/chandler/livepeer-gateway) AND only on a platform-official cluster;
// every other case returns (nil,""). This also exercises
// isPlatformGlobalRootServiceType's true/false branches.
func TestPlatformGlobalRootIngressDomainsForService(t *testing.T) {
	manifest := &inventory.Manifest{
		RootDomain: "frameworks.network",
		Clusters: map[string]inventory.ClusterConfig{
			"official": {PlatformOfficial: true},
			"private":  {Class: "tenant_private"},
		},
	}

	t.Run("platform pool service on official cluster", func(t *testing.T) {
		domains, bundleID := PlatformGlobalRootIngressDomainsForService("foghorn", inventory.ServiceConfig{}, manifest, "official")
		wantFQDN, ok := pkgdns.BunnyRootServiceFQDN("foghorn", manifest.RootDomain)
		if !ok {
			t.Fatalf("precondition: BunnyRootServiceFQDN(foghorn) not resolvable")
		}
		if !slices.Equal(domains, []string{wantFQDN}) {
			t.Fatalf("domains = %v, want %v", domains, []string{wantFQDN})
		}
		if bundleID != TLSBundleID("wildcard", manifest.RootDomain) {
			t.Fatalf("bundleID = %q, want %q", bundleID, TLSBundleID("wildcard", manifest.RootDomain))
		}
	})

	t.Run("pool service on non-official cluster is suppressed", func(t *testing.T) {
		domains, bundleID := PlatformGlobalRootIngressDomainsForService("foghorn", inventory.ServiceConfig{}, manifest, "private")
		if domains != nil || bundleID != "" {
			t.Fatalf("non-official = (%v,%q), want (nil,\"\")", domains, bundleID)
		}
	})

	t.Run("non-pool service is suppressed even on official cluster", func(t *testing.T) {
		domains, bundleID := PlatformGlobalRootIngressDomainsForService("bridge", inventory.ServiceConfig{}, manifest, "official")
		if domains != nil || bundleID != "" {
			t.Fatalf("non-pool service = (%v,%q), want (nil,\"\")", domains, bundleID)
		}
	})
}
