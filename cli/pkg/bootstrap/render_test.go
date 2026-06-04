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
				PublicTopology:   true,
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
	if !c.IsDefault || !c.IsPlatformOfficial || !c.PublicTopology {
		t.Fatalf("cluster flags = (default=%v, platform_official=%v, public_topology=%v); all should be true", c.IsDefault, c.IsPlatformOfficial, c.PublicTopology)
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
			CustomerBilling: []CustomerBilling{
				{Tenant: TenantRefAlias("northwind"), Model: "prepaid", Tier: "developer", ClusterAccess: "derived"},
			},
		},
		Accounts: []AccountDerived{
			{
				Kind:   AccountCustomer,
				Tenant: TenantRefAlias("northwind"),
				Users:  []AccountUserDerived{{AccountUserCommon: AccountUserCommon{Email: "admin@northwind.example", Role: "owner"}}},
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
	if got := len(r.Purser.CustomerBilling); got != 1 {
		t.Fatalf("expected 1 explicit customer_billing row, got %d", got)
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

func TestDeriveInfrastructureRegistryForMeshTopology(t *testing.T) {
	m := minimalManifest()
	m.Hosts["db-1"] = inventory.Host{Cluster: "core-central-primary", ExternalIP: "203.0.113.11", WireguardIP: "10.99.0.11", WireguardPublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="}
	m.Hosts["kafka-1"] = inventory.Host{Cluster: "core-central-primary", ExternalIP: "203.0.113.12", WireguardIP: "10.99.0.12", WireguardPublicKey: "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="}
	m.Infrastructure.Postgres = &inventory.PostgresConfig{
		Enabled: true,
		Engine:  "yugabyte",
		Nodes:   []inventory.PostgresNode{{Host: "db-1", ID: 1}},
	}
	m.Infrastructure.Kafka = &inventory.KafkaConfig{
		Enabled:   true,
		ClusterID: "kafka-core",
		Brokers:   []inventory.KafkaBroker{{Host: "kafka-1", ID: 1, Port: 9092}},
	}

	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	got := map[string]string{}
	for _, e := range d.Quartermaster.ServiceRegistry {
		got[e.ServiceName+"@"+e.NodeID] = e.Type
	}
	if got["postgres-1@db-1"] != "database" {
		t.Fatalf("postgres infra registry missing: %v", got)
	}
	if got["kafka-kafka-core-1@kafka-1"] != "kafka" {
		t.Fatalf("kafka infra registry missing: %v", got)
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

	sites := map[string]IngressSite{}
	for _, s := range d.Quartermaster.Ingress.Sites {
		sites[s.ID] = s
	}
	site, ok := sites["livepeer-gateway-core-eu-1-media-free-eu"]
	if !ok {
		t.Fatalf("missing logical ingress site; got %+v", sites)
	}
	if site.ClusterID != "core-eu" {
		t.Fatalf("ingress site cluster_id = %q, want physical cluster core-eu", site.ClusterID)
	}
	if site.NodeID != "core-eu-1" {
		t.Fatalf("ingress site node_id = %q, want core-eu-1", site.NodeID)
	}
	if !slices.Equal(site.Domains, []string{"livepeer.media-free-eu.frameworks.network"}) {
		t.Fatalf("ingress site domains = %v, want logical media-cluster domain", site.Domains)
	}
	if site.TLSBundleID != "wildcard-media-free-eu-frameworks-network" {
		t.Fatalf("ingress site tls_bundle_id = %q, want logical media-cluster bundle", site.TLSBundleID)
	}

	// Physical per-instance endpoint: a SEPARATE site + exact-SAN bundle keyed
	// on the node, independent of media-cluster assignment.
	const physFQDN = "livepeer-gateway.core-eu-1.infra.frameworks.network"
	const physBundleID = "physical-livepeer-gateway-core-eu-1-infra-frameworks-network"
	phys, ok := sites["livepeer-gateway-core-eu-1-physical"]
	if !ok {
		t.Fatalf("missing physical ingress site; got %+v", sites)
	}
	if phys.Kind != "physical" {
		t.Fatalf("physical site kind = %q, want physical", phys.Kind)
	}
	if phys.ClusterID != "core-eu" || phys.NodeID != "core-eu-1" {
		t.Fatalf("physical site cluster/node = %q/%q, want core-eu/core-eu-1", phys.ClusterID, phys.NodeID)
	}
	if !slices.Equal(phys.Domains, []string{physFQDN}) {
		t.Fatalf("physical site domains = %v, want [%s]", phys.Domains, physFQDN)
	}
	if phys.TLSBundleID != physBundleID {
		t.Fatalf("physical site tls_bundle_id = %q, want %q", phys.TLSBundleID, physBundleID)
	}
	pb, ok := bundles[physBundleID]
	if !ok {
		t.Fatalf("missing physical TLS bundle %s; got %+v", physBundleID, bundles)
	}
	if !slices.Equal(pb.Domains, []string{physFQDN}) {
		t.Fatalf("physical bundle domains = %v, want exact SAN [%s]", pb.Domains, physFQDN)
	}
}

func TestDeriveVMAUTHDefaultsIngressToAllMediaClusters(t *testing.T) {
	m := minimalManifest()
	m.RootDomain = "frameworks.network"
	m.Hosts["regional-eu-1"] = inventory.Host{ExternalIP: "203.0.113.10", Cluster: "regional-eu"}
	m.Clusters = map[string]inventory.ClusterConfig{
		"regional-eu": {Name: "Regional EU", Type: "regional"},
		"media-eu-1":  {Name: "Media EU 1", Type: "edge", Default: true, Roles: []string{"media"}},
		"media-us-1":  {Name: "Media US 1", Type: "edge", Roles: []string{"media"}},
	}
	m.Observability = map[string]inventory.ServiceConfig{
		"vmauth": {
			Enabled: true,
			Host:    "regional-eu-1",
		},
	}

	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	sites := map[string]IngressSite{}
	for _, s := range d.Quartermaster.Ingress.Sites {
		sites[s.ID] = s
	}
	for _, clusterID := range []string{"media-eu-1", "media-us-1"} {
		siteID := "vmauth-regional-eu-1-" + clusterID
		site, ok := sites[siteID]
		if !ok {
			t.Fatalf("missing vmauth ingress site %s; got %+v", siteID, sites)
		}
		wantDomains := []string{"telemetry." + clusterID + ".frameworks.network"}
		if !slices.Equal(site.Domains, wantDomains) {
			t.Fatalf("%s domains = %v, want %v", siteID, site.Domains, wantDomains)
		}
		if site.TLSBundleID != "wildcard-"+strings.ReplaceAll(clusterID+".frameworks.network", ".", "-") {
			t.Fatalf("%s tls_bundle_id = %q", siteID, site.TLSBundleID)
		}
	}
	registryEntries := map[string]ServiceRegistryEntry{}
	for _, entry := range d.Quartermaster.ServiceRegistry {
		registryEntries[entry.ServiceName] = entry
	}
	entry, ok := registryEntries["vmauth"]
	if !ok {
		t.Fatalf("missing vmauth service_registry entry; got %+v", registryEntries)
	}
	if entry.Type != "vmauth" {
		t.Fatalf("vmauth service_registry type = %q, want vmauth", entry.Type)
	}
	if entry.Port != 8427 {
		t.Fatalf("vmauth service_registry port = %d, want default 8427", entry.Port)
	}
}

func TestDeriveVMAUTHIngressScopesRegionalHosts(t *testing.T) {
	m := minimalManifest()
	m.RootDomain = "frameworks.network"
	m.Hosts["regional-eu-1"] = inventory.Host{ExternalIP: "203.0.113.10", Cluster: "regional-eu"}
	m.Hosts["regional-us-1"] = inventory.Host{ExternalIP: "203.0.113.20", Cluster: "regional-us"}
	m.Clusters = map[string]inventory.ClusterConfig{
		"regional-eu": {Name: "Regional EU", Type: "regional", Region: "eu-west"},
		"regional-us": {Name: "Regional US", Type: "regional", Region: "us-east"},
		"media-eu-1":  {Name: "Media EU 1", Type: "edge", Default: true, Roles: []string{"media"}, Region: "eu-west"},
		"media-us-1":  {Name: "Media US 1", Type: "edge", Roles: []string{"media"}, Region: "us-east"},
	}
	m.Observability = map[string]inventory.ServiceConfig{
		"vmauth": {
			Enabled: true,
			Hosts:   []string{"regional-eu-1", "regional-us-1"},
		},
	}

	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	sites := map[string]IngressSite{}
	for _, s := range d.Quartermaster.Ingress.Sites {
		sites[s.ID] = s
	}
	euSite, ok := sites["vmauth-regional-eu-1-media-eu-1"]
	if !ok {
		t.Fatalf("missing EU vmauth site; got %+v", sites)
	}
	if !slices.Equal(euSite.Domains, []string{"telemetry.media-eu-1.frameworks.network"}) {
		t.Fatalf("EU domains = %v", euSite.Domains)
	}
	if _, exists := sites["vmauth-regional-eu-1-media-us-1"]; exists {
		t.Fatal("EU vmauth host must not publish US telemetry ingress")
	}
	usSite, ok := sites["vmauth-regional-us-1-media-us-1"]
	if !ok {
		t.Fatalf("missing US vmauth site; got %+v", sites)
	}
	if !slices.Equal(usSite.Domains, []string{"telemetry.media-us-1.frameworks.network"}) {
		t.Fatalf("US domains = %v", usSite.Domains)
	}
	if _, exists := sites["vmauth-regional-us-1-media-eu-1"]; exists {
		t.Fatal("US vmauth host must not publish EU telemetry ingress")
	}
}

func TestDeriveChandlerLogicalIngressUsesPhysicalNodeCluster(t *testing.T) {
	m := minimalManifest()
	m.RootDomain = "frameworks.network"
	m.Hosts["core-eu-1"] = inventory.Host{
		ExternalIP:  "203.0.113.10",
		WireguardIP: "10.99.0.1",
		Cluster:     "core-eu",
	}
	m.Clusters = map[string]inventory.ClusterConfig{
		"core-eu":       {Name: "Core EU", Type: "central"},
		"media-free-eu": {Name: "Media Free EU", Type: "edge", Roles: []string{"media"}, Default: true},
	}
	m.Services = map[string]inventory.ServiceConfig{
		"chandler": {
			Enabled: true,
			Host:    "core-eu-1",
			Port:    18020,
		},
	}

	d, err := Derive(m, DeriveOptions{SharedEnv: map[string]string{"ACME_EMAIL": "ops@example.com"}})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(d.Quartermaster.ServiceRegistry) != 0 {
		t.Fatalf("chandler self-registers; bootstrap must not pre-seed service_registry, got %+v", d.Quartermaster.ServiceRegistry)
	}

	sites := map[string]IngressSite{}
	for _, s := range d.Quartermaster.Ingress.Sites {
		sites[s.ID] = s
	}
	site, ok := sites["chandler-core-eu-1-media-free-eu"]
	if !ok {
		t.Fatalf("missing chandler logical ingress site; got %+v", sites)
	}
	if site.ClusterID != "core-eu" {
		t.Fatalf("ingress site cluster_id = %q, want physical cluster core-eu", site.ClusterID)
	}
	if !slices.Equal(site.Domains, []string{"chandler.media-free-eu.frameworks.network"}) {
		t.Fatalf("ingress site domains = %v, want logical media-cluster domain", site.Domains)
	}
	if site.TLSBundleID != "wildcard-media-free-eu-frameworks-network" {
		t.Fatalf("ingress site tls_bundle_id = %q, want logical media-cluster bundle", site.TLSBundleID)
	}
}

func TestDerivePlatformPoolServicePublishesGlobalRootIngress(t *testing.T) {
	m := minimalManifest()
	m.RootDomain = "frameworks.network"
	m.Hosts["regional-eu-1"] = inventory.Host{
		ExternalIP:  "203.0.113.10",
		WireguardIP: "10.99.0.10",
		Cluster:     "regional-eu",
	}
	m.Clusters = map[string]inventory.ClusterConfig{
		"regional-eu": {Name: "Regional EU", Type: "central"},
		"media-eu-1":  {Name: "Media EU 1", Type: "edge", Roles: []string{"media"}, PlatformOfficial: true},
	}
	m.Services = map[string]inventory.ServiceConfig{
		"foghorn-eu": {
			Enabled: true,
			Deploy:  "foghorn",
			Host:    "regional-eu-1",
			Cluster: "media-eu-1",
			Port:    18008,
		},
	}

	d, err := Derive(m, DeriveOptions{SharedEnv: map[string]string{"ACME_EMAIL": "ops@example.com"}})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	sites := map[string]IngressSite{}
	for _, s := range d.Quartermaster.Ingress.Sites {
		sites[s.ID] = s
	}
	site, ok := sites["foghorn-eu-regional-eu-1-global-root"]
	if !ok {
		t.Fatalf("missing global root foghorn ingress site; got %+v", sites)
	}
	if !slices.Equal(site.Domains, []string{"foghorn.frameworks.network"}) {
		t.Fatalf("global root foghorn domains = %v", site.Domains)
	}
	if site.TLSBundleID != "wildcard-frameworks-network" {
		t.Fatalf("global root foghorn tls_bundle_id = %q", site.TLSBundleID)
	}
	if site.Upstream.Host != "10.99.0.10" || site.Upstream.Port != 18008 {
		t.Fatalf("global root foghorn upstream = %+v", site.Upstream)
	}

	bundles := map[string]TLSBundle{}
	for _, b := range d.Quartermaster.Ingress.TLSBundles {
		bundles[b.ID] = b
	}
	bundle, ok := bundles["wildcard-frameworks-network"]
	if !ok {
		t.Fatalf("missing root wildcard bundle; got %+v", bundles)
	}
	if !slices.Equal(bundle.Domains, []string{"frameworks.network", "*.frameworks.network"}) {
		t.Fatalf("root wildcard bundle domains = %v", bundle.Domains)
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

func TestDeriveLivepeerGatewayAliasUsesCanonicalServiceRegistry(t *testing.T) {
	m := minimalManifest()
	m.Clusters["media-us-1"] = inventory.ClusterConfig{Name: "Media US 1", Type: "edge", Roles: []string{"media"}}
	m.Services = map[string]inventory.ServiceConfig{
		"livepeer-gateway-us": {
			Enabled: true,
			Deploy:  "livepeer-gateway",
			Host:    "core-eu-1",
			Port:    8935,
			Cluster: "media-us-1",
			Config:  map[string]string{"eth_acct_addr": "0xdef456"},
		},
	}
	d, err := Derive(m, DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if len(d.Quartermaster.ServiceRegistry) != 1 {
		t.Fatalf("expected one service_registry entry, got %+v", d.Quartermaster.ServiceRegistry)
	}
	entry := d.Quartermaster.ServiceRegistry[0]
	if entry.ServiceName != "livepeer-gateway" || entry.Type != "livepeer-gateway" {
		t.Fatalf("registry identity = %q/%q, want canonical livepeer-gateway", entry.ServiceName, entry.Type)
	}
	if entry.Metadata["wallet_address"] != "0xdef456" {
		t.Fatalf("wallet metadata missing from alias entry: %+v", entry.Metadata)
	}

	var found bool
	for _, site := range d.Quartermaster.Ingress.Sites {
		if slices.Equal(site.Domains, []string{"livepeer.media-us-1.frameworks.network"}) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing media-us-1 livepeer ingress site: %+v", d.Quartermaster.Ingress.Sites)
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

func TestDeriveDedupesRootTLSBundlesAcrossPhysicalClusters(t *testing.T) {
	m := minimalManifest()
	m.Hosts = map[string]inventory.Host{
		"core-eu-1": {
			ExternalIP:  "203.0.113.10",
			WireguardIP: "10.99.0.1",
			Cluster:     "core-central-primary",
		},
		"regional-eu-1": {
			ExternalIP:  "203.0.113.20",
			WireguardIP: "10.99.1.1",
			Cluster:     "regional-eu-primary",
		},
		"regional-us-1": {
			ExternalIP:  "203.0.113.30",
			WireguardIP: "10.99.2.1",
			Cluster:     "regional-us-primary",
		},
	}
	m.Clusters = map[string]inventory.ClusterConfig{
		"core-central-primary": {
			Name:             "Core Central Primary",
			Type:             "central",
			Default:          true,
			PlatformOfficial: true,
			OwnerTenant:      "frameworks",
		},
		"regional-eu-primary": {
			Name:             "Regional EU Primary",
			Type:             "central",
			Roles:            []string{"services", "interface", "mesh"},
			PlatformOfficial: true,
			OwnerTenant:      "frameworks",
		},
		"regional-us-primary": {
			Name:             "Regional US Primary",
			Type:             "central",
			Roles:            []string{"services", "interface", "mesh"},
			PlatformOfficial: true,
			OwnerTenant:      "frameworks",
		},
		"media-eu-1": {
			Name:        "Media EU 1",
			Type:        "edge",
			Roles:       []string{"media"},
			OwnerTenant: "frameworks",
		},
		"media-us-1": {
			Name:        "Media US 1",
			Type:        "edge",
			Roles:       []string{"media"},
			OwnerTenant: "frameworks",
		},
	}
	m.Services = map[string]inventory.ServiceConfig{
		"chartroom": {Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}, Port: 18030},
	}
	m.Interfaces = map[string]inventory.ServiceConfig{
		"foredeck": {Enabled: true, Hosts: []string{"regional-eu-1", "regional-us-1"}, Port: 18080},
	}

	d, err := Derive(m, DeriveOptions{SharedEnv: map[string]string{"ACME_EMAIL": "ops@example.com"}})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	r, err := Render(d, nil, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	counts := map[string]int{}
	bundles := map[string]TLSBundle{}
	for _, b := range d.Quartermaster.Ingress.TLSBundles {
		counts[b.ID]++
		bundles[b.ID] = b
	}
	for _, id := range []string{"wildcard-frameworks-network", "apex-frameworks-network"} {
		if counts[id] != 1 {
			t.Fatalf("%s count = %d, want 1; bundles=%+v", id, counts[id], d.Quartermaster.Ingress.TLSBundles)
		}
		if got := bundles[id].ClusterID; got != "core-central-primary" {
			t.Fatalf("%s cluster_id = %q, want core-central-primary", id, got)
		}
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

// manifestWithMedia returns minimalManifest plus an edge cluster, since
// pull-stream validation requires at least one media-capable cluster.
func manifestWithMedia(allowPrivate bool) *inventory.Manifest {
	m := minimalManifest()
	m.Clusters["media-edge-primary"] = inventory.ClusterConfig{
		Name:                    "Media Edge Primary",
		Type:                    "edge",
		PlatformOfficial:        true,
		OwnerTenant:             "frameworks",
		Roles:                   []string{"media"},
		AllowPrivatePullSources: allowPrivate,
	}
	m.Hosts["media-eu-1"] = inventory.Host{
		Name:               "media-eu-1",
		ExternalIP:         "203.0.113.20",
		User:               "root",
		WireguardIP:        "10.99.0.2",
		WireguardPublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		WireguardPort:      51820,
		Cluster:            "media-edge-primary",
	}
	return m
}

func TestRenderPullStreamValidatesSourceURI(t *testing.T) {
	d, err := Derive(manifestWithMedia(false), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:  "frameworks-demo",
				OwnerTenant: TenantRefSystem(),
				Title:       "FrameWorks marketing demo",
				SourceURI:   "https://ntv1.akamaized.net/hls/live/2014075/NASA-NTV1-HLS/master.m3u8",
				Enabled:     true,
			}},
		},
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got := len(r.Commodore.PullStreams); got != 1 {
		t.Fatalf("pull streams = %d, want 1", got)
	}
	ps := r.Commodore.PullStreams[0]
	if ps.SourceURI != "https://ntv1.akamaized.net/hls/live/2014075/NASA-NTV1-HLS/master.m3u8" {
		t.Fatalf("SourceURI = %q", ps.SourceURI)
	}
}

func TestRenderPullStreamRejectsUnsupportedSourceURI(t *testing.T) {
	d, err := Derive(manifestWithMedia(false), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:  "bad-demo",
				OwnerTenant: TenantRefSystem(),
				Title:       "Bad demo",
				SourceURI:   "https://example.com/live",
				Enabled:     true,
			}},
		},
	}
	if _, err := Render(d, overlay, nil); err == nil || !strings.Contains(err.Error(), "source_uri") {
		t.Fatalf("expected source_uri validation error, got %v", err)
	}
}

// TestRenderPullStreamRejectsPrivateSourceWithoutAllowedClusters locks the
// new architecture rule: a private URI must list explicit allowed_cluster_ids;
// "any cluster with allow_private_pull_sources=true" is no longer a fallback.
func TestRenderPullStreamRejectsPrivateSourceWithoutAllowedClusters(t *testing.T) {
	d, err := Derive(manifestWithMedia(true), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:  "private-demo",
				OwnerTenant: TenantRefSystem(),
				Title:       "Private demo",
				SourceURI:   "tsudp://10.0.0.5:9000",
				Enabled:     true,
			}},
		},
	}
	_, err = Render(d, overlay, nil)
	if err == nil {
		t.Fatal("private URI without allowed_cluster_ids must fail render")
	}
	if !strings.Contains(err.Error(), "allowed_cluster_ids") {
		t.Fatalf("error %q does not name the missing field", err)
	}
}

// TestRenderPullStreamRejectsPrivateSourceWithoutCapability covers the
// stricter check: an explicit allowed_cluster_ids entry must point at a
// cluster that also carries allow_private_pull_sources=true.
func TestRenderPullStreamRejectsPrivateSourceWithoutCapability(t *testing.T) {
	d, err := Derive(manifestWithMedia(false), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:        "private-demo",
				OwnerTenant:       TenantRefSystem(),
				Title:             "Private demo",
				SourceURI:         "tsudp://10.0.0.5:9000",
				AllowedClusterIDs: []string{"media-edge-primary"},
				Enabled:           true,
			}},
		},
	}
	_, err = Render(d, overlay, nil)
	if err == nil {
		t.Fatal("private URI pinned to a cluster without capability must fail render")
	}
	if !strings.Contains(err.Error(), "allow_private_pull_sources") {
		t.Fatalf("error %q does not name the capability flag", err)
	}
}

// TestRenderPullStreamAcceptsPrivateSourceWithAllowedCluster confirms the
// happy path: a private URI pinned to a cluster that has the capability
// flag passes render with a normalized allowed_cluster_ids slice.
func TestRenderPullStreamAcceptsPrivateSourceWithAllowedCluster(t *testing.T) {
	d, err := Derive(manifestWithMedia(true), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:        "private-demo",
				OwnerTenant:       TenantRefSystem(),
				Title:             "Private demo",
				SourceURI:         "tsudp://10.0.0.5:9000",
				AllowedClusterIDs: []string{"media-edge-primary", "media-edge-primary"}, // dedup
				Enabled:           true,
			}},
		},
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(r.Commodore.PullStreams) != 1 {
		t.Fatalf("pull streams = %d, want 1", len(r.Commodore.PullStreams))
	}
	got := r.Commodore.PullStreams[0].AllowedClusterIDs
	if len(got) != 1 || got[0] != "media-edge-primary" {
		t.Fatalf("allowed_cluster_ids = %v, want [media-edge-primary] (deduped)", got)
	}
}

// TestRenderPullStreamRejectsUnknownAllowedCluster covers the unknown-ID
// branch: an allowed_cluster_ids entry that does not match any registered
// edge cluster must fail render.
func TestRenderPullStreamRejectsUnknownAllowedCluster(t *testing.T) {
	d, err := Derive(manifestWithMedia(true), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:        "pinned-demo",
				OwnerTenant:       TenantRefSystem(),
				Title:             "Pinned demo",
				SourceURI:         "https://example.com/stream.m3u8",
				AllowedClusterIDs: []string{"ghost-cluster"},
				Enabled:           true,
			}},
		},
	}
	_, err = Render(d, overlay, nil)
	if err == nil {
		t.Fatal("unknown allowed_cluster_ids entry must fail render")
	}
	if !strings.Contains(err.Error(), "ghost-cluster") {
		t.Fatalf("error %q does not name the offending ID", err)
	}
}

// TestRenderPullStreamPinsPublicSource covers explicit public-source pinning:
// an HTTPS source with a non-empty allowed_cluster_ids should pass render
// and propagate the sorted/normalized list.
func TestRenderPullStreamPinsPublicSource(t *testing.T) {
	d, err := Derive(manifestWithMedia(false), DeriveOptions{})
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	overlay := &Overlay{
		Commodore: CommodoreSection{
			PullStreams: []PullStream{{
				PlaybackID:        "public-pinned",
				OwnerTenant:       TenantRefSystem(),
				Title:             "Public pinned",
				SourceURI:         "https://example.com/stream.m3u8",
				AllowedClusterIDs: []string{"media-edge-primary"},
				Enabled:           true,
			}},
		},
	}
	r, err := Render(d, overlay, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := r.Commodore.PullStreams[0].AllowedClusterIDs
	if len(got) != 1 || got[0] != "media-edge-primary" {
		t.Fatalf("allowed_cluster_ids = %v, want [media-edge-primary]", got)
	}
}
