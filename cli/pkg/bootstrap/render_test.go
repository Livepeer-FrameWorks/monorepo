package bootstrap

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// minimalManifest builds a representative single-cluster manifest used as the baseline
// fixture for derive/render/validate tests. Mirrors what `cluster provision` would
// build for a small dev cluster: one central cluster, one core node, WireGuard mesh
// enabled, root domain set, pricing declared.
func minimalManifest() *inventory.Manifest {
	pricingTier := 2
	return &inventory.Manifest{
		Version:    "1",
		Type:       "cluster",
		Profile:    "control-plane",
		RootDomain: "frameworks.network",
		WireGuard: &inventory.WireGuardConfig{
			Enabled:    true,
			MeshCIDR:   "10.99.0.0/16",
			ListenPort: 51820,
		},
		Hosts: map[string]inventory.Host{
			"core-eu-1": {
				Name:               "core-eu-1",
				ExternalIP:         "203.0.113.10",
				User:               "root",
				WireguardIP:        "10.99.0.1",
				WireguardPublicKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				WireguardPort:      51820,
				Cluster:            "core-central-primary",
			},
		},
		Clusters: map[string]inventory.ClusterConfig{
			"core-central-primary": {
				Name:             "Core Central Primary",
				Type:             "central",
				Default:          true,
				PlatformOfficial: true,
				OwnerTenant:      "frameworks",
				Pricing: &inventory.ClusterPricingConfig{
					Model:             "tiered",
					RequiredTierLevel: &pricingTier,
				},
			},
		},
	}
}

func TestDeriveProducesSystemTenantAndCluster(t *testing.T) {
	m := minimalManifest()
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	if d.Quartermaster.SystemTenant == nil {
		t.Fatal("expected system_tenant in derived state")
	}
	if d.Quartermaster.SystemTenant.Alias != "frameworks" {
		t.Fatalf("system_tenant.id = %q, want frameworks", d.Quartermaster.SystemTenant.Alias)
	}

	if got := len(d.Quartermaster.Clusters); got != 1 {
		t.Fatalf("expected 1 cluster, got %d", got)
	}
	c := d.Quartermaster.Clusters[0]
	if c.ID != "core-central-primary" || c.OwnerTenant.Ref != "quartermaster.system_tenant" {
		t.Fatalf("cluster = %+v, want id=core-central-primary owner_tenant=system_tenant", c)
	}
	if c.Mesh.CIDR != "10.99.0.0/16" {
		t.Fatalf("cluster.mesh.cidr = %q, want 10.99.0.0/16", c.Mesh.CIDR)
	}
	if !c.IsDefault || !c.IsPlatformOfficial {
		t.Fatalf("cluster flags = (default=%v, platform_official=%v); both should be true", c.IsDefault, c.IsPlatformOfficial)
	}

	if got := len(d.Quartermaster.Nodes); got != 1 {
		t.Fatalf("expected 1 node, got %d", got)
	}
	if d.Quartermaster.Nodes[0].ClusterID != "core-central-primary" {
		t.Fatalf("node.cluster_id = %q", d.Quartermaster.Nodes[0].ClusterID)
	}

	if got := len(d.Purser.ClusterPricing); got != 1 {
		t.Fatalf("expected 1 cluster_pricing, got %d", got)
	}
	if d.Purser.ClusterPricing[0].PricingModel != "tiered" {
		t.Fatalf("pricing model = %q", d.Purser.ClusterPricing[0].PricingModel)
	}
}

func TestDeriveSystemTenantClusterAccessAlwaysSet(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if d.Quartermaster.SystemTenantClusterAccess == nil {
		t.Fatal("system_tenant_cluster_access must be set so quartermaster bootstrap reconciles it after clusters exist")
	}
	if !d.Quartermaster.SystemTenantClusterAccess.DefaultClusters || !d.Quartermaster.SystemTenantClusterAccess.PlatformOfficialClusters {
		t.Fatalf("system_tenant_cluster_access flags should both default to true")
	}
}

func TestRenderOverlayAddsCustomerTenantAndCluster(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	overlay := &Overlay{
		Quartermaster: QuartermasterSection{
			Tenants: []Tenant{
				{Alias: "northwind", Name: "Northwind Traders"},
			},
			Clusters: []Cluster{
				{
					ID:          "northwind-private-eu",
					Name:        "Northwind Private EU",
					Type:        "edge",
					OwnerTenant: TenantRefAlias("northwind"),
					Mesh:        ClusterMesh{CIDR: "10.99.16.0/20", ListenPort: 51820},
				},
			},
		},
		Purser: PurserSection{
			ClusterPricing: []ClusterPricing{
				{ClusterID: "northwind-private-eu", PricingModel: "flat", BasePrice: "499.00", Currency: "USD"},
			},
		},
		Accounts: []AccountDerived{
			{
				Kind:    AccountCustomer,
				Tenant:  TenantRefAlias("northwind"),
				Users:   []AccountUserDerived{{AccountUserCommon: AccountUserCommon{Email: "admin@northwind.example", Role: "owner"}}},
				Billing: AccountBilling{Mode: "prepaid", Tier: "developer", ClusterAccess: "derived"},
			},
		},
	}

	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if got := len(r.Quartermaster.Tenants); got != 1 {
		t.Fatalf("expected 1 customer tenant, got %d", got)
	}
	if got := len(r.Quartermaster.Clusters); got != 2 {
		t.Fatalf("expected 2 clusters (1 derived + 1 overlay), got %d", got)
	}
	if got := len(r.Purser.ClusterPricing); got != 2 {
		t.Fatalf("expected 2 cluster_pricing rows, got %d", got)
	}
	if got := len(r.Accounts); got != 1 {
		t.Fatalf("expected 1 account, got %d", got)
	}
}

func TestRenderOverlayCollisionWithoutOverrideRejected(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	overlay := &Overlay{
		Quartermaster: QuartermasterSection{
			Clusters: []Cluster{
				{
					ID:          "core-central-primary", // collides with derived
					Name:        "Renamed Cluster",
					Type:        "central",
					OwnerTenant: TenantRefSystem(),
					Mesh:        ClusterMesh{CIDR: "10.99.0.0/16"},
					// Override deliberately not set — should be rejected.
				},
			},
		},
	}

	_, err = Render(d, overlay, nil)
	if err == nil {
		t.Fatal("expected error when overlay collides with derived cluster without override")
	}
	if !strings.Contains(err.Error(), "override: true") {
		t.Fatalf("error should mention the override hint; got %v", err)
	}
}

func TestRenderOverlayOverrideReplacesDerivedCluster(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	overlay := &Overlay{
		Quartermaster: QuartermasterSection{
			Clusters: []Cluster{
				{
					ID:          "core-central-primary",
					Name:        "Renamed Cluster",
					Type:        "central",
					OwnerTenant: TenantRefSystem(),
					Mesh:        ClusterMesh{CIDR: "10.99.0.0/16"},
					Override:    true,
				},
			},
		},
	}

	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := len(r.Quartermaster.Clusters); got != 1 {
		t.Fatalf("expected 1 cluster after override, got %d", got)
	}
	if r.Quartermaster.Clusters[0].Name != "Renamed Cluster" {
		t.Fatalf("override didn't replace name: %+v", r.Quartermaster.Clusters[0])
	}
	// Override marker should not survive into the rendered output.
	if r.Quartermaster.Clusters[0].Override {
		t.Fatal("override marker leaked into rendered output")
	}
}

func TestRenderRejectsNilDerived(t *testing.T) {
	if _, err := Render(nil, nil, nil); err == nil {
		t.Fatal("expected error on nil derived")
	}
}

func TestDeriveRejectsNilManifest(t *testing.T) {
	if _, err := Derive(nil, DeriveOptions{}); err == nil {
		t.Fatal("expected error on nil manifest")
	}
}

// TestDeriveWalksAllServiceMaps verifies that public services in Services,
// Interfaces, and Observability all contribute service_registry rows.
func TestDeriveWalksAllServiceMaps(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"chandler": {Enabled: true, Host: "core-eu-1", Port: 18020},
	}
	m.Interfaces = map[string]inventory.ServiceConfig{
		"chartroom": {Enabled: true, Host: "core-eu-1", Port: 18030},
	}
	m.Observability = map[string]inventory.ServiceConfig{
		"vmauth": {Enabled: true, Host: "core-eu-1", Port: 8427},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	names := map[string]bool{}
	for _, e := range d.Quartermaster.ServiceRegistry {
		names[e.ServiceName] = true
	}
	for _, want := range []string{"chandler", "chartroom", "vmauth"} {
		if !names[want] {
			t.Errorf("expected %q in service_registry; got %v", want, names)
		}
	}
}

// TestDeriveServiceRegistryFillsServicedefs exercises the servicedefs.Lookup
// integration: HealthEndpoint and Protocol come from the canonical registry, not
// from manifest input.
func TestDeriveServiceRegistryFillsServicedefs(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		// Manifest omits Port — servicedefs DefaultPort kicks in.
		"chandler": {Enabled: true, Host: "core-eu-1"},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 registry entry; got %d", got)
	}
	e := d.Quartermaster.ServiceRegistry[0]
	if e.Port != 18020 {
		t.Errorf("Port = %d, want 18020 (servicedef default)", e.Port)
	}
	if e.HealthEndpoint != "/health" {
		t.Errorf("HealthEndpoint = %q, want /health (servicedef)", e.HealthEndpoint)
	}
	if e.Protocol != "http" {
		t.Errorf("Protocol = %q, want http (servicedef)", e.Protocol)
	}
}

// TestDeriveAdoptsExplicitTLSBundlesAndSites pins manifest TLSBundles +
// IngressSites: explicit entries appear in the rendered output, and identical
// auto-derived ids defer to the explicit one.
func TestDeriveAdoptsExplicitTLSBundlesAndSites(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"chandler": {Enabled: true, Host: "core-eu-1", Port: 18020},
	}
	m.TLSBundles = map[string]inventory.TLSBundleConfig{
		"explicit-bundle": {
			Cluster: "core-central-primary",
			Domains: []string{"explicit.frameworks.network"},
			Issuer:  "lets-encrypt",
			Email:   "ops@example.com",
		},
	}
	m.IngressSites = map[string]inventory.IngressSiteConfig{
		"explicit-site": {
			Cluster:     "core-central-primary",
			Node:        "core-eu-1",
			Domains:     []string{"explicit.frameworks.network"},
			TLSBundleID: "explicit-bundle",
			Kind:        "http",
			Upstream:    "10.99.0.1:8080",
		},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	var sawExplicitBundle bool
	for _, b := range d.Quartermaster.Ingress.TLSBundles {
		if b.ID == "explicit-bundle" {
			sawExplicitBundle = true
			if b.Issuer != "lets-encrypt" || b.Email != "ops@example.com" {
				t.Errorf("explicit bundle metadata not preserved: %+v", b)
			}
		}
	}
	if !sawExplicitBundle {
		t.Fatalf("expected explicit TLS bundle in derived state; got %+v", d.Quartermaster.Ingress.TLSBundles)
	}

	var sawExplicitSite bool
	for _, s := range d.Quartermaster.Ingress.Sites {
		if s.ID == "explicit-site" {
			sawExplicitSite = true
			if s.Upstream.Host != "10.99.0.1" || s.Upstream.Port != 8080 {
				t.Errorf("explicit site upstream not parsed: %+v", s.Upstream)
			}
		}
	}
	if !sawExplicitSite {
		t.Fatalf("expected explicit ingress site in derived state; got %+v", d.Quartermaster.Ingress.Sites)
	}
}

// TestDeriveEmitsRegistryEntryPerHost pins the multi-host fix: a public service
// deployed across multiple hosts must produce one ServiceRegistryEntry per host
// (matching production NodeId: task.Host registration behavior).
func TestDeriveEmitsRegistryEntryPerHost(t *testing.T) {
	m := minimalManifest()
	m.Hosts["core-eu-2"] = inventory.Host{
		Name:               "core-eu-2",
		ExternalIP:         "203.0.113.11",
		User:               "root",
		WireguardIP:        "10.99.0.2",
		WireguardPublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		WireguardPort:      51820,
		Cluster:            "core-central-primary",
	}
	m.Services = map[string]inventory.ServiceConfig{
		"chandler": {Enabled: true, Hosts: []string{"core-eu-1", "core-eu-2"}, Port: 18020},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	nodes := map[string]bool{}
	for _, e := range d.Quartermaster.ServiceRegistry {
		if e.ServiceName == "chandler" {
			nodes[e.NodeID] = true
		}
	}
	for _, host := range []string{"core-eu-1", "core-eu-2"} {
		if !nodes[host] {
			t.Errorf("expected service_registry entry for chandler@%s; got %v", host, nodes)
		}
	}
}

// TestDeriveLivepeerGatewayMetadata pins the manifest-derivable Livepeer
// metadata: public_host (host external IP) + public_port (manifest service
// port). Admin endpoints are intentionally not modeled — operator transport
// handles that. wallet_address requires SecretRef-backed metadata, which the
// schema doesn't carry today; see render.go's deriveServiceMetadata.
func TestDeriveLivepeerGatewayMetadata(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"livepeer-gateway": {Enabled: true, Host: "core-eu-1", Port: 8935},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 registry entry; got %d", got)
	}
	md := d.Quartermaster.ServiceRegistry[0].Metadata
	if md["public_host"] != "203.0.113.10" {
		t.Errorf("public_host = %q, want 203.0.113.10", md["public_host"])
	}
	if md["public_port"] != "8935" {
		t.Errorf("public_port = %q, want 8935", md["public_port"])
	}
	for k := range md {
		if k != "public_host" && k != "public_port" {
			t.Errorf("unexpected metadata key %q (admin endpoints must not appear; wallet_address needs SecretRef support)", k)
		}
	}
}

func TestDeriveProducesIngressAndServiceRegistry(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"bridge":    {Enabled: true, Host: "core-eu-1", Port: 18008},
		"chandler":  {Enabled: true, Host: "core-eu-1", Port: 18012},
		"navigator": {Enabled: false, Host: "core-eu-1", Port: 18010},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 service_registry entry (chandler); got %d (%+v)", got, d.Quartermaster.ServiceRegistry)
	}
	if d.Quartermaster.ServiceRegistry[0].ServiceName != "chandler" {
		t.Fatalf("expected chandler in registry; got %q", d.Quartermaster.ServiceRegistry[0].ServiceName)
	}
	if got := len(d.Quartermaster.Ingress.Sites); got != 2 {
		t.Fatalf("expected 2 ingress sites (bridge + chandler); got %d (%+v)", got, d.Quartermaster.Ingress.Sites)
	}
	for _, s := range d.Quartermaster.Ingress.Sites {
		if s.Upstream.Host != "10.99.0.1" {
			t.Fatalf("site %q upstream host = %q, want mesh IP 10.99.0.1", s.ID, s.Upstream.Host)
		}
	}
	if got := len(d.Quartermaster.Ingress.TLSBundles); got == 0 {
		t.Fatal("expected at least one TLS bundle")
	}
}

func TestDeriveSupportsImplicitSingleClusterManifest(t *testing.T) {
	m := minimalManifest()
	m.Clusters = nil
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.Clusters); got != 1 {
		t.Fatalf("expected 1 implicit cluster; got %d", got)
	}
	if id := d.Quartermaster.Clusters[0].ID; id != "cluster-control-plane" {
		t.Fatalf("expected auto-generated id cluster-control-plane; got %q", id)
	}
}

func TestRenderOverrideRejectsStableFieldChange(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Quartermaster: QuartermasterSection{
			Clusters: []Cluster{{
				ID:          "core-central-primary",
				Name:        "Renamed",
				Type:        "central",
				OwnerTenant: TenantRefAlias("ghost"),
				Mesh:        ClusterMesh{CIDR: "10.99.0.0/16"},
				Override:    true,
			}},
		},
	}
	if _, err := Render(d, overlay, nil); err == nil || !strings.Contains(err.Error(), "owner_tenant is stable") {
		t.Fatalf("expected owner_tenant stability error; got %v", err)
	}
	overlay.Quartermaster.Clusters[0].OwnerTenant = TenantRefSystem()
	overlay.Quartermaster.Clusters[0].Mesh.CIDR = "10.50.0.0/16"
	if _, err := Render(d, overlay, nil); err == nil || !strings.Contains(err.Error(), "mesh.cidr is stable") {
		t.Fatalf("expected mesh.cidr stability error; got %v", err)
	}
}

func TestRenderOverridePreservesMutableFields(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Quartermaster: QuartermasterSection{
			Clusters: []Cluster{{
				ID:          "core-central-primary",
				Name:        "Renamed",
				Region:      "eu-west",
				OwnerTenant: TenantRefSystem(),
				Mesh:        ClusterMesh{CIDR: "10.99.0.0/16", ListenPort: 51821},
				Override:    true,
			}},
		},
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if r.Quartermaster.Clusters[0].Name != "Renamed" || r.Quartermaster.Clusters[0].Region != "eu-west" {
		t.Fatalf("mutable fields not updated: %+v", r.Quartermaster.Clusters[0])
	}
	if r.Quartermaster.Clusters[0].Mesh.ListenPort != 51821 {
		t.Fatalf("mesh.listen_port not updated: %+v", r.Quartermaster.Clusters[0])
	}
}

func TestRenderClusterPricingOverrideRequired(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Purser: PurserSection{
			ClusterPricing: []ClusterPricing{{ClusterID: "core-central-primary", PricingModel: "flat"}},
		},
	}
	if _, rerr := Render(d, overlay, nil); rerr == nil || !strings.Contains(rerr.Error(), "override: true") {
		t.Fatalf("expected pricing override error; got %v", rerr)
	}
	overlay.Purser.ClusterPricing[0].Override = true
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render with override: %v", err)
	}
	if r.Purser.ClusterPricing[0].PricingModel != "flat" {
		t.Fatalf("pricing model not overridden: %+v", r.Purser.ClusterPricing[0])
	}
}

// TestRenderClusterPricingOverrideIsFieldLevel pins the field-level merge: an
// overlay that sets only PricingModel must keep the derived RequiredTierLevel and
// other unset fields, not wipe them.
func TestRenderClusterPricingOverrideIsFieldLevel(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if d.Purser.ClusterPricing[0].RequiredTierLevel == nil {
		t.Fatal("fixture should have a derived RequiredTierLevel")
	}
	derivedTier := *d.Purser.ClusterPricing[0].RequiredTierLevel

	overlay := &Overlay{
		Purser: PurserSection{
			ClusterPricing: []ClusterPricing{{
				ClusterID:    "core-central-primary",
				PricingModel: "metered",
				Override:     true,
				// RequiredTierLevel intentionally unset — must inherit from derived.
			}},
		},
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := r.Purser.ClusterPricing[0]
	if got.PricingModel != "metered" {
		t.Errorf("PricingModel not overridden: %q", got.PricingModel)
	}
	if got.RequiredTierLevel == nil || *got.RequiredTierLevel != derivedTier {
		t.Errorf("RequiredTierLevel was wiped: %v (want preserved %d)", got.RequiredTierLevel, derivedTier)
	}
}

func TestRenderBillingTierOverlay(t *testing.T) {
	d, err := Derive(minimalManifest(), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Purser: PurserSection{
			BillingTiers: []BillingTier{{ID: "enterprise-custom", DisplayName: "Enterprise Custom"}},
		},
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := len(r.Purser.BillingTiers); got != 1 || r.Purser.BillingTiers[0].ID != "enterprise-custom" {
		t.Fatalf("billing_tiers overlay not applied: %+v", r.Purser.BillingTiers)
	}
}
