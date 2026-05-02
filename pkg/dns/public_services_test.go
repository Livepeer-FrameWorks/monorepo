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
