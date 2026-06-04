package dns

import (
	"slices"
	"testing"
)

func TestManagedServiceTypesIncludesLivepeerGateway(t *testing.T) {
	if !slices.Contains(ManagedServiceTypes(), "livepeer-gateway") {
		t.Fatal("ManagedServiceTypes() should include livepeer-gateway")
	}
	if !slices.Contains(ManagedServiceTypes(), "chandler") {
		t.Fatal("ManagedServiceTypes() should include chandler")
	}
	if !slices.Contains(ManagedServiceTypes(), "telemetry") {
		t.Fatal("ManagedServiceTypes() should include telemetry")
	}
	if !slices.Contains(ManagedServiceTypes(), "grafana") {
		t.Fatal("ManagedServiceTypes() should include grafana")
	}
	if !slices.Contains(ManagedServiceTypes(), "metabase") {
		t.Fatal("ManagedServiceTypes() should include metabase")
	}
}

func TestProviderForServiceType(t *testing.T) {
	tests := []struct {
		serviceType string
		want        Provider
	}{
		{serviceType: "edge-ingest", want: ProviderBunny},
		{serviceType: "foghorn", want: ProviderBunny},
		{serviceType: "chandler", want: ProviderBunny},
		{serviceType: "bridge", want: ProviderCloudflare},
		{serviceType: "chartroom", want: ProviderCloudflare},
		{serviceType: "grafana", want: ProviderCloudflare},
		{serviceType: "signalman", want: ProviderNone},
		{serviceType: "unknown", want: ProviderNone},
	}

	for _, tt := range tests {
		t.Run(tt.serviceType, func(t *testing.T) {
			if got := ProviderForServiceType(tt.serviceType); got != tt.want {
				t.Fatalf("ProviderForServiceType(%q) = %q, want %q", tt.serviceType, got, tt.want)
			}
		})
	}
}

func TestManagedServiceTypesByProvider(t *testing.T) {
	bunny := BunnyManagedServiceTypes()
	if !slices.Contains(bunny, "edge-ingest") {
		t.Fatal("BunnyManagedServiceTypes() should include edge-ingest")
	}
	if slices.Contains(bunny, "bridge") {
		t.Fatal("BunnyManagedServiceTypes() should not include bridge")
	}

	cloudflare := CloudflareManagedServiceTypes()
	if !slices.Contains(cloudflare, "bridge") {
		t.Fatal("CloudflareManagedServiceTypes() should include bridge")
	}
	if slices.Contains(cloudflare, "foghorn") {
		t.Fatal("CloudflareManagedServiceTypes() should not include foghorn")
	}
}

func TestPublicSubdomain(t *testing.T) {
	tests := []struct {
		serviceType string
		want        string
		ok          bool
	}{
		{serviceType: "chandler", want: "chandler", ok: true},
		{serviceType: "telemetry", want: "telemetry", ok: true},
		{serviceType: "livepeer-gateway", want: "livepeer", ok: true},
		{serviceType: "foghorn", want: "foghorn", ok: true},
		{serviceType: "grafana", want: "grafana", ok: true},
		{serviceType: "foredeck", want: "", ok: true},
		{serviceType: "unknown", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.serviceType, func(t *testing.T) {
			got, ok := PublicSubdomain(tt.serviceType)
			if ok != tt.ok {
				t.Fatalf("PublicSubdomain(%q) ok=%v, want %v", tt.serviceType, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("PublicSubdomain(%q) = %q, want %q", tt.serviceType, got, tt.want)
			}
		})
	}
}

func TestServiceFQDN(t *testing.T) {
	tests := []struct {
		serviceType string
		rootDomain  string
		want        string
		ok          bool
	}{
		{serviceType: "chandler", rootDomain: "example.com", want: "chandler.example.com", ok: true},
		{serviceType: "telemetry", rootDomain: "example.com", want: "telemetry.example.com", ok: true},
		{serviceType: "chandler", rootDomain: "cluster-a.example.com", want: "chandler.cluster-a.example.com", ok: true},
		{serviceType: "livepeer-gateway", rootDomain: "cluster-a.example.com", want: "livepeer.cluster-a.example.com", ok: true},
		{serviceType: "grafana", rootDomain: "example.com", want: "grafana.example.com", ok: true},
		{serviceType: "foredeck", rootDomain: "example.com", want: "example.com", ok: true},
		{serviceType: "unknown", rootDomain: "example.com", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.serviceType+"_"+tt.rootDomain, func(t *testing.T) {
			got, ok := ServiceFQDN(tt.serviceType, tt.rootDomain)
			if ok != tt.ok {
				t.Fatalf("ServiceFQDN(%q, %q) ok=%v, want %v", tt.serviceType, tt.rootDomain, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("ServiceFQDN(%q, %q) = %q, want %q", tt.serviceType, tt.rootDomain, got, tt.want)
			}
		})
	}
}

func TestNormalizeDomainScope(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "bare domain", in: "frameworks.network", want: "frameworks.network"},
		{name: "https url", in: "https://frameworks.network", want: "frameworks.network"},
		{name: "https url with slash", in: "https://frameworks.network/", want: "frameworks.network"},
		{name: "https url missing colon", in: "https//frameworks.network", want: "frameworks.network"},
		{name: "url with path", in: "https://frameworks.network/clusters/media", want: "frameworks.network"},
		{name: "protocol relative", in: "//frameworks.network", want: "frameworks.network"},
		{name: "host port", in: "frameworks.network:443", want: "frameworks.network"},
		{name: "mixed case", in: "HTTPS://FrameWorks.Network", want: "frameworks.network"},
		{name: "empty", in: " ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeDomainScope(tt.in); got != tt.want {
				t.Fatalf("NormalizeDomainScope(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestServiceFQDNNormalizesDomainScope(t *testing.T) {
	got, ok := ServiceFQDN("chandler", "https://frameworks.network/")
	if !ok || got != "chandler.frameworks.network" {
		t.Fatalf("ServiceFQDN normalized URL scope = %q, %v; want chandler.frameworks.network true", got, ok)
	}
}

func TestRootServiceFQDNRejectsMediaServices(t *testing.T) {
	if got, ok := RootServiceFQDN("livepeer-gateway", "example.com"); ok || got != "" {
		t.Fatalf("RootServiceFQDN(livepeer-gateway) = %q, %v; want empty false", got, ok)
	}
	if got, ok := RootServiceFQDN("edge-ingest", "example.com"); ok || got != "" {
		t.Fatalf("RootServiceFQDN(edge-ingest) = %q, %v; want empty false", got, ok)
	}
	if got, ok := RootServiceFQDN("bridge", "example.com"); !ok || got != "bridge.example.com" {
		t.Fatalf("RootServiceFQDN(bridge) = %q, %v; want bridge.example.com true", got, ok)
	}
}

func TestIsClusterScopedServiceType(t *testing.T) {
	if !IsClusterScopedServiceType("chandler") {
		t.Fatal("chandler should be cluster-scoped")
	}
	if !IsClusterScopedServiceType("livepeer-gateway") {
		t.Fatal("livepeer-gateway should be cluster-scoped")
	}
	if !IsClusterScopedServiceType("telemetry") {
		t.Fatal("telemetry should be cluster-scoped")
	}
	if IsClusterScopedServiceType("bridge") {
		t.Fatal("bridge should not be cluster-scoped")
	}
}

func TestPoolAssignedServiceTypesIncludesVMAUTH(t *testing.T) {
	if !slices.Contains(PoolAssignedServiceTypes(), "vmauth") {
		t.Fatal("PoolAssignedServiceTypes() should include vmauth")
	}
	if !IsPoolAssignedServiceType("vmauth") {
		t.Fatal("vmauth should be pool-assigned")
	}
	if IsPoolAssignedServiceType("telemetry") {
		t.Fatal("telemetry is the public DNS name; vmauth is the assigned backing service")
	}
}

func TestClusterSlug(t *testing.T) {
	tests := []struct {
		name        string
		clusterID   string
		clusterName string
		want        string
	}{
		{name: "id wins", clusterID: "media-central-primary", clusterName: "Media Central", want: "media-central-primary"},
		{name: "fallback to name", clusterID: "", clusterName: "Media Central", want: "media-central"},
		{name: "default id falls back to name", clusterID: "___", clusterName: "Media Central", want: "media-central"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClusterSlug(tt.clusterID, tt.clusterName)
			if got != tt.want {
				t.Fatalf("ClusterSlug(%q, %q) = %q, want %q", tt.clusterID, tt.clusterName, got, tt.want)
			}
		})
	}
}

func TestIsReservedTenantSlug(t *testing.T) {
	clusters := []string{"media-us-1", "media-eu-1"}
	cases := []struct {
		name string
		slug string
		want bool
	}{
		{"empty rejected", "", true},
		{"default rejected", "default", true},
		{"www reserved", "www", true},
		{"cdn reserved (the tenant zone label)", "cdn", true},
		{"api reserved", "api", true},
		{"mcp reserved", "mcp", true},
		{"managed service foghorn", "foghorn", true},
		{"managed service edge-ingest", "edge-ingest", true},
		{"public subdomain livepeer (label != service name)", "livepeer", true},
		{"operator service bridge", "bridge", true},
		{"active cluster slug", "media-us-1", true},
		{"prefix edge- reserved", "edge-mysite", true},
		{"normal tenant", "acme", false},
		{"normal tenant with dash", "bobs-streams", false},
		{"numeric ok", "tenant42", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsReservedTenantSlug(tc.slug, clusters); got != tc.want {
				t.Errorf("IsReservedTenantSlug(%q) = %v, want %v", tc.slug, got, tc.want)
			}
		})
	}
}

func TestReservedTenantSlugsIncludesEverything(t *testing.T) {
	got := ReservedTenantSlugs([]string{"media-us-1"})
	mustHave := []string{
		"foghorn", "chandler", "edge-ingest", "edge-egress", "livepeer",
		"bridge", "grafana", "logbook", "cdn", "www", "api", "mcp",
		"media-us-1",
	}
	gotSet := make(map[string]bool)
	for _, s := range got {
		gotSet[s] = true
	}
	for _, want := range mustHave {
		if !gotSet[want] {
			t.Errorf("ReservedTenantSlugs missing %q", want)
		}
	}
}

// The pooled DNS wake must use the DNS-facing name: vmauth's public record is
// telemetry.<cluster> (Navigator only remaps telemetry->vmauth for lookup), so a
// wake passing "vmauth" would produce no record. The rest are identity.
func TestPoolDNSWakeServiceType(t *testing.T) {
	cases := map[string]string{
		"vmauth":           "telemetry",
		"foghorn":          "foghorn",
		"chandler":         "chandler",
		"livepeer-gateway": "livepeer-gateway",
	}
	for in, want := range cases {
		if got := PoolDNSWakeServiceType(in); got != want {
			t.Fatalf("PoolDNSWakeServiceType(%q) = %q, want %q", in, got, want)
		}
	}
	// Every pool-assigned instance type maps to a managed (DNS-reconciled) type.
	for _, instType := range PoolAssignedServiceTypes() {
		wake := PoolDNSWakeServiceType(instType)
		if !slices.Contains(ManagedServiceTypes(), wake) {
			t.Fatalf("wake type %q (from %q) is not in ManagedServiceTypes()", wake, instType)
		}
	}
}
