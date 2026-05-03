package bootstrap

import (
	"slices"
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

// TestDeriveWalksAllServiceMaps verifies that non-self-registering public
// services in Services, Interfaces, and Observability all contribute
// service_registry rows. Self-registering services (bridge/foghorn/chandler)
// are intentionally absent from the pre-seed — runtime BootstrapService
// creates their rows.
func TestDeriveWalksAllServiceMaps(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"chatwoot": {Enabled: true, Host: "core-eu-1", Port: 18092},
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
	for _, want := range []string{"chatwoot", "chartroom", "vmauth"} {
		if !names[want] {
			t.Errorf("expected %q in service_registry; got %v", want, names)
		}
	}
}

// TestDeriveServiceRegistryFillsServicedefs exercises the servicedefs.Lookup
// integration: HealthEndpoint and Protocol come from the canonical registry, not
// from manifest input. Uses chartroom because chandler self-registers and is
// therefore not pre-seeded by Derive.
func TestDeriveServiceRegistryFillsServicedefs(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		// Manifest omits Port — servicedefs DefaultPort kicks in.
		"chartroom": {Enabled: true, Host: "core-eu-1"},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 registry entry; got %d", got)
	}
	e := d.Quartermaster.ServiceRegistry[0]
	if e.Port != 18030 {
		t.Errorf("Port = %d, want 18030 (servicedef default)", e.Port)
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
// (matching production NodeId: task.Host registration behavior). Uses chartroom
// because chandler self-registers and is therefore not pre-seeded.
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
		"chartroom": {Enabled: true, Hosts: []string{"core-eu-1", "core-eu-2"}, Port: 18030},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	nodes := map[string]bool{}
	for _, e := range d.Quartermaster.ServiceRegistry {
		if e.ServiceName == "chartroom" {
			nodes[e.NodeID] = true
		}
	}
	for _, host := range []string{"core-eu-1", "core-eu-2"} {
		if !nodes[host] {
			t.Errorf("expected service_registry entry for chartroom@%s; got %v", host, nodes)
		}
	}
}

// TestDeriveLivepeerGatewayMetadata pins the rendered metadata: invariant
// fields (public_scheme, public_port, wallet_address) come from the manifest;
// the cluster-derived public_host is NOT stored, because the same gateway
// pool may serve multiple media clusters and DiscoverServices synthesizes the
// per-cluster URL at request time from service_cluster_assignments.
func TestDeriveLivepeerGatewayMetadata(t *testing.T) {
	m := minimalManifest()
	m.RootDomain = "frameworks.network"
	m.Hosts["core-eu-1"] = inventory.Host{ExternalIP: "203.0.113.10", Cluster: "core-eu"}
	m.Clusters = map[string]inventory.ClusterConfig{
		"core-eu": {Name: "Core EU"},
	}
	m.Services = map[string]inventory.ServiceConfig{
		"livepeer-gateway": {
			Enabled: true,
			Host:    "core-eu-1",
			Port:    8935,
		},
	}
	opts := DeriveOptions{SharedEnv: map[string]string{"LIVEPEER_ETH_ACCT_ADDR": "0xabc123"}}
	d, err := Derive(m, opts)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 registry entry; got %d", got)
	}
	md := d.Quartermaster.ServiceRegistry[0].Metadata
	if _, ok := md["public_host"]; ok {
		t.Errorf("public_host must not be stored as service metadata under M:N — got %q", md["public_host"])
	}
	if md["public_port"] != "443" {
		t.Errorf("public_port = %q, want 443", md["public_port"])
	}
	if md["public_scheme"] != "https" {
		t.Errorf("public_scheme = %q, want https", md["public_scheme"])
	}
	if md["wallet_address"] != "0xabc123" {
		t.Errorf("wallet_address = %q, want 0xabc123", md["wallet_address"])
	}
	for k := range md {
		if k != "public_port" && k != "public_scheme" && k != "wallet_address" {
			t.Errorf("unexpected metadata key %q (cluster-derived fields and admin endpoints must not appear in derived metadata)", k)
		}
	}
}

func TestDeriveLivepeerGatewayPhysicalRegistryAndLogicalTLSBundles(t *testing.T) {
	m := minimalManifest()
	m.RootDomain = "frameworks.network"
	m.Hosts["core-eu-1"] = inventory.Host{ExternalIP: "203.0.113.10", Cluster: "core-eu"}
	m.Clusters = map[string]inventory.ClusterConfig{
		"core-eu":       {Name: "Core EU", Type: "central"},
		"media-free-eu": {Name: "Media Free EU", Type: "edge", Roles: []string{"media"}},
		"media-paid-eu": {Name: "Media Paid EU", Type: "edge", Roles: []string{"media"}},
	}
	m.Services = map[string]inventory.ServiceConfig{
		"livepeer-gateway": {
			Enabled:  true,
			Host:     "core-eu-1",
			Port:     8935,
			Clusters: []string{"media-free-eu", "media-paid-eu"},
		},
	}

	d, err := Derive(m, DeriveOptions{SharedEnv: map[string]string{"LIVEPEER_ETH_ACCT_ADDR": "0xabc123"}})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 registry entry; got %d", got)
	}
	if got := d.Quartermaster.ServiceRegistry[0].ClusterID; got != "core-eu" {
		t.Fatalf("service_registry cluster_id = %q, want physical cluster core-eu", got)
	}

	bundles := map[string]TLSBundle{}
	for _, b := range d.Quartermaster.Ingress.TLSBundles {
		bundles[b.ID] = b
	}
	for _, clusterID := range []string{"media-free-eu", "media-paid-eu"} {
		id := "wildcard-" + strings.ReplaceAll(clusterID+".frameworks.network", ".", "-")
		b, ok := bundles[id]
		if !ok {
			t.Fatalf("missing TLS bundle %s; got %+v", id, bundles)
		}
		want := []string{clusterID + ".frameworks.network", "*." + clusterID + ".frameworks.network"}
		if !slices.Equal(b.Domains, want) {
			t.Fatalf("%s domains = %v, want %v", id, b.Domains, want)
		}
	}
}

// TestDeriveLivepeerGatewayWalletFromServiceConfig confirms the manifest
// authority path: putting eth_acct_addr in services.livepeer-gateway.config
// works even when shared env doesn't carry the value.
func TestDeriveLivepeerGatewayWalletFromServiceConfig(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"livepeer-gateway": {
			Enabled: true,
			Host:    "core-eu-1",
			Port:    8935,
			Config:  map[string]string{"eth_acct_addr": "0xdef456"},
		},
	}
	d, err := Derive(m, DeriveOptions{}) // no SharedEnv
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	md := d.Quartermaster.ServiceRegistry[0].Metadata
	if md["wallet_address"] != "0xdef456" {
		t.Errorf("wallet_address = %q, want 0xdef456 (sourced from service config)", md["wallet_address"])
	}
}

// TestDeriveLivepeerGatewayWalletMissingFailsValidate confirms the fail-loud
// path: a livepeer-gateway entry without a resolvable wallet_address must
// fail Validate(), so render time catches a misconfigured operator instead
// of Purser silently skipping the gateway at runtime.
func TestDeriveLivepeerGatewayWalletMissingFailsValidate(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"livepeer-gateway": {Enabled: true, Host: "core-eu-1", Port: 8935},
	}
	d, err := Derive(m, DeriveOptions{}) // no SharedEnv
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	r, err := Render(d, nil, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	verr := r.Validate()
	if verr == nil {
		t.Fatal("expected Validate error for livepeer-gateway without wallet_address")
	}
	if !strings.Contains(verr.Error(), "wallet_address") {
		t.Errorf("expected wallet_address in error, got %v", verr)
	}
}

func TestDeriveProducesIngressAndServiceRegistry(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"bridge":    {Enabled: true, Host: "core-eu-1", Port: 18008},
		"chartroom": {Enabled: true, Host: "core-eu-1", Port: 18030},
		"navigator": {Enabled: false, Host: "core-eu-1", Port: 18010},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	// bridge self-registers, so only chartroom is pre-seeded.
	if got := len(d.Quartermaster.ServiceRegistry); got != 1 {
		t.Fatalf("expected 1 service_registry entry (chartroom); got %d (%+v)", got, d.Quartermaster.ServiceRegistry)
	}
	if d.Quartermaster.ServiceRegistry[0].ServiceName != "chartroom" {
		t.Fatalf("expected chartroom in registry; got %q", d.Quartermaster.ServiceRegistry[0].ServiceName)
	}
	if got := len(d.Quartermaster.Ingress.Sites); got != 2 {
		t.Fatalf("expected 2 ingress sites (bridge + chartroom); got %d (%+v)", got, d.Quartermaster.Ingress.Sites)
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

// TestDeriveAutoTLSBundlesScopedPerRoot pins the per-bundle SAN scoping: the apex
// wildcard bundle covers only the apex root + apex wildcard, and the cluster-scoped
// wildcard bundle covers only the cluster root + cluster wildcard. They must not
// share SAN sets. This is what gives nginx a cert that validates for the public
// FQDN it serves (e.g. chatwoot.frameworks.network or chandler.<cluster>.frameworks.network).
func TestDeriveAutoTLSBundlesScopedPerRoot(t *testing.T) {
	m := minimalManifest()
	m.Services = map[string]inventory.ServiceConfig{
		"chatwoot": {Enabled: true, Host: "core-eu-1", Port: 18092},
		"chandler": {Enabled: true, Host: "core-eu-1", Port: 18020},
	}
	d, err := Derive(m, DeriveOptions{SharedEnv: map[string]string{"ACME_EMAIL": "ops@example.com"}})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	bundles := map[string]TLSBundle{}
	for _, b := range d.Quartermaster.Ingress.TLSBundles {
		bundles[b.ID] = b
	}

	wantApex := []string{"frameworks.network", "*.frameworks.network"}
	gotApex, ok := bundles["wildcard-frameworks-network"]
	if !ok {
		t.Fatalf("expected bundle wildcard-frameworks-network; got %+v", bundles)
	}
	if !slices.Equal(gotApex.Domains, wantApex) {
		t.Errorf("wildcard-frameworks-network domains = %v, want %v", gotApex.Domains, wantApex)
	}
	if gotApex.Email != "ops@example.com" || gotApex.Issuer != "navigator" {
		t.Errorf("wildcard-frameworks-network issuer/email = %q/%q, want navigator/ops@example.com", gotApex.Issuer, gotApex.Email)
	}

	wantCluster := []string{
		"core-central-primary.frameworks.network",
		"*.core-central-primary.frameworks.network",
	}
	gotCluster, ok := bundles["wildcard-core-central-primary-frameworks-network"]
	if !ok {
		t.Fatalf("expected bundle wildcard-core-central-primary-frameworks-network; got %+v", bundles)
	}
	if !slices.Equal(gotCluster.Domains, wantCluster) {
		t.Errorf("wildcard-core-central-primary-frameworks-network domains = %v, want %v", gotCluster.Domains, wantCluster)
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
