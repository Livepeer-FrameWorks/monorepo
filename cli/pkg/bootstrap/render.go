package bootstrap

import (
	"fmt"
	"slices"
	"strings"

	"frameworks/cli/pkg/clusterderive"
	"frameworks/cli/pkg/inventory"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// DeriveOptions carries values the manifest doesn't express on its own. The
// BootstrapAdmin block, when set, becomes a system_operator account in the derived
// state.
type DeriveOptions struct {
	BootstrapAdmin *BootstrapAdminSpec
	// SharedEnv is the operator's shared env file, propagated unmodified so
	// service-specific derivation can reach values that aren't in cluster.yaml
	// service env_vars (e.g. LIVEPEER_ETH_ACCT_ADDR for livepeer-gateway's
	// wallet_address metadata).
	SharedEnv map[string]string
}

// BootstrapAdminSpec is the operator-account inputs for a system_operator account.
// PasswordRef is required (the resolver fills it in Render); the rest is metadata.
type BootstrapAdminSpec struct {
	Email       string
	Role        string // defaults to "owner" when empty
	FirstName   string
	LastName    string
	PasswordRef SecretRef
}

// Derive builds the manifest-derived layer of the bootstrap state from the cluster
// manifest. It does not consult overlays or resolve secrets — its output is layer-3.
func Derive(m *inventory.Manifest, opts DeriveOptions) (*Derived, error) {
	if m == nil {
		return nil, fmt.Errorf("bootstrap.Derive: nil manifest")
	}
	d := &Derived{}

	// DeploymentTier is intentionally unset: the column is billing-derived
	// (Purser stamps billing_tiers.tier_name) and bootstrap must not claim a
	// tier for the system tenant — 'global' used to pass the alias-eligibility
	// gates and provisioned a *.cdn alias for the platform account.
	d.Quartermaster.SystemTenant = &Tenant{
		Alias:          SystemTenantAlias,
		Name:           "FrameWorks",
		PrimaryColor:   "#6366f1",
		SecondaryColor: "#f59e0b",
	}

	d.Quartermaster.SystemTenantClusterAccess = &SystemTenantClusterAccess{
		DefaultClusters:          true,
		PlatformOfficialClusters: true,
	}

	// Clusters — iterate AllClusterIDs to handle manifests without an explicit
	// `clusters:` section (single auto-generated id from type+profile).
	clusterIDs := m.AllClusterIDs()
	for _, clusterID := range clusterIDs {
		cc, hasCfg := m.Clusters[clusterID]
		c := Cluster{
			ID:          clusterID,
			OwnerTenant: deriveOwnerTenantRef(cc.OwnerTenant),
			BaseURL:     deriveClusterBaseURL(m),
		}
		if hasCfg {
			c.Name = cc.Name
			c.Type = cc.Type
			c.Region = cc.Region
			c.IsDefault = cc.Default
			c.IsPlatformOfficial = cc.PlatformOfficial
			c.PublicTopology = cc.PublicTopology
			c.AllowPrivatePullSources = cc.AllowPrivatePullSources
			c.Cell = cc.Cell
			c.Class = cc.Class
			c.ControlCell = cc.ControlCell
			c.EligibleServingCells = cc.EligibleServingCells
			c.S3Bucket = cc.S3Bucket
			c.S3Endpoint = cc.S3Endpoint
			c.S3Region = cc.S3Region
		}
		if c.Type == "" {
			c.Type = m.Type
		}
		if c.Type == "" {
			c.Type = "central"
		}
		// Defaults applied at render time so the rendered desired-state file is
		// self-describing and the QM-side reconciler doesn't have to re-derive.
		// Platform-official clusters self-control: cell = id, class derives
		// from platform_official, control_cell = cell, eligible_serving = [cell].
		if c.Cell == "" {
			c.Cell = clusterID
		}
		if c.Class == "" && c.IsPlatformOfficial {
			c.Class = "platform_official"
		}
		if c.ControlCell == "" {
			c.ControlCell = c.Cell
		}
		if len(c.EligibleServingCells) == 0 && c.ControlCell != "" {
			c.EligibleServingCells = []string{c.ControlCell}
		}
		if m.WireGuard != nil {
			c.Mesh = ClusterMesh{
				CIDR:       m.WireGuard.MeshCIDR,
				ListenPort: m.WireGuard.ListenPort,
			}
		}
		d.Quartermaster.Clusters = append(d.Quartermaster.Clusters, c)

		if hasCfg && cc.Pricing != nil {
			d.Purser.ClusterPricing = append(d.Purser.ClusterPricing, ClusterPricing{
				ClusterID:         clusterID,
				PricingModel:      cc.Pricing.Model,
				RequiredTierLevel: int32PtrFromIntPtr(cc.Pricing.RequiredTierLevel),
				AllowFreeTier:     cc.Pricing.AllowFreeTier,
				DefaultQuotas:     intMapToAny(cc.Pricing.DefaultQuotas),
			})
		}
	}

	if m.WireGuard != nil && m.WireGuard.Enabled {
		d.Quartermaster.Mesh = &Mesh{
			CIDR:            m.WireGuard.MeshCIDR,
			ListenPort:      m.WireGuard.ListenPort,
			ManageHostsFile: m.WireGuard.ManageHostsFile,
		}
	}

	// Nodes — every host with a WireGuard identity becomes an infrastructure node.
	for hostName, h := range m.Hosts {
		if h.WireguardIP == "" && h.WireguardPublicKey == "" {
			continue
		}
		clusterID := m.HostCluster(hostName)
		nodeType := "core"
		if slices.Contains(h.Roles, "edge") {
			nodeType = "edge"
		}
		d.Quartermaster.Nodes = append(d.Quartermaster.Nodes, Node{
			ID:         hostName,
			ClusterID:  clusterID,
			Type:       nodeType,
			ExternalIP: h.ExternalIP,
			WireGuard: NodeWireGuard{
				IP:        h.WireguardIP,
				PublicKey: h.WireguardPublicKey,
				Port:      h.WireguardPort,
			},
		})
	}

	deriveInfrastructureRegistry(d, m)

	// Service registry + ingress sites + TLS bundles, derived from manifest.Services.
	deriveIngressAndRegistry(d, m, opts)

	if opts.BootstrapAdmin != nil {
		role := opts.BootstrapAdmin.Role
		if role == "" {
			role = "owner"
		}
		d.Accounts = append(d.Accounts, AccountDerived{
			Kind:   AccountSystemOperator,
			Tenant: TenantRef{Ref: "quartermaster.system_tenant"},
			Users: []AccountUserDerived{{
				AccountUserCommon: AccountUserCommon{
					Email:     opts.BootstrapAdmin.Email,
					Role:      role,
					FirstName: opts.BootstrapAdmin.FirstName,
					LastName:  opts.BootstrapAdmin.LastName,
				},
				PasswordRef: opts.BootstrapAdmin.PasswordRef,
			}},
			Billing: AccountBilling{Mode: "none"},
		})
	}

	return d, nil
}

func deriveInfrastructureRegistry(d *Derived, m *inventory.Manifest) {
	if d == nil || m == nil {
		return
	}
	addEntry := func(serviceName, serviceType, host string, port int, metadata map[string]string) {
		if serviceName == "" || serviceType == "" || host == "" || port == 0 {
			return
		}
		clusterID := serviceClusterIDForHost(m, serviceName, host)
		if clusterID == "" {
			return
		}
		d.Quartermaster.ServiceRegistry = append(d.Quartermaster.ServiceRegistry, ServiceRegistryEntry{
			ServiceName: serviceName,
			Type:        serviceType,
			Protocol:    "tcp",
			ClusterID:   clusterID,
			NodeID:      host,
			Port:        port,
			Metadata:    metadata,
		})
	}
	infraMetadata := func(kind, role, name string) map[string]string {
		md := map[string]string{
			"topology_provider": "infra",
			"peer_only":         "true",
			"infra_kind":        kind,
			"infra_role":        role,
		}
		if name != "" {
			md["infra_name"] = name
		}
		return md
	}

	if pg := m.Infrastructure.Postgres; pg != nil && pg.Enabled {
		port := pg.EffectivePort()
		if len(pg.Nodes) > 0 {
			for _, node := range pg.Nodes {
				addEntry(fmt.Sprintf("postgres-%d", node.ID), "database", node.Host, port, infraMetadata("database", "primary", ""))
			}
		} else {
			addEntry("postgres", "database", pg.Host, port, infraMetadata("database", "primary", ""))
		}
		for _, inst := range pg.Instances {
			instPort := inst.Port
			if instPort == 0 {
				instPort = port
			}
			addEntry("postgres-"+safeServiceNamePart(inst.Name), "database", inst.Host, instPort, infraMetadata("database", "named", inst.Name))
		}
	}

	if kafka := m.Infrastructure.Kafka; kafka != nil && kafka.Enabled {
		addKafka := func(label, role string, brokers []inventory.KafkaBroker) {
			for _, broker := range brokers {
				port := broker.Port
				if port == 0 {
					port = 9092
				}
				md := infraMetadata("kafka", role, "")
				md["kafka_region_id"] = label
				addEntry(fmt.Sprintf("kafka-%s-%d", safeServiceNamePart(label), broker.ID), "kafka", broker.Host, port, md)
			}
		}
		label := kafka.RegionID
		if label == "" {
			label = kafka.ClusterID
		}
		if label == "" {
			label = "primary"
		}
		addKafka(label, kafkaProviderRole(kafka.Role, "aggregator"), kafka.Brokers)
		for _, regional := range kafka.Regional {
			label := regional.RegionID
			if label == "" {
				label = regional.ClusterID
			}
			addKafka(label, kafkaProviderRole(regional.Role, "regional"), regional.Brokers)
		}
	}

	if ch := m.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
		port := ch.Port
		if port == 0 {
			port = 9000
		}
		addEntry("clickhouse", "clickhouse", ch.Host, port, infraMetadata("clickhouse", "primary", ""))
	}

	if redis := m.Infrastructure.Redis; redis != nil && redis.Enabled {
		for _, inst := range redis.Instances {
			port := inst.Port
			if port == 0 {
				port = 6379
			}
			namePart := safeServiceNamePart(inst.Name)
			addEntry("redis-"+namePart, "redis", inst.Host, port, infraMetadata("redis", "primary", inst.Name))
			for i, host := range inst.ReplicaHosts {
				addEntry(fmt.Sprintf("redis-%s-replica-%d", namePart, i+1), "redis", host, port, infraMetadata("redis", "replica", inst.Name))
			}
			for i, sentinel := range inst.Sentinels {
				sentinelPort := sentinel.Port
				if sentinelPort == 0 {
					sentinelPort = 26379
				}
				addEntry(fmt.Sprintf("redis-%s-sentinel-%d", namePart, i+1), "redis", sentinel.Host, sentinelPort, infraMetadata("redis", "sentinel", inst.Name))
			}
		}
	}
}

func kafkaProviderRole(role, defaultRole string) string {
	role = strings.TrimSpace(strings.ToLower(role))
	if role == "" {
		return defaultRole
	}
	return role
}

func safeServiceNamePart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "default"
	}
	replacer := strings.NewReplacer(" ", "-", "_", "-", ".", "-", "/", "-", ":", "-")
	return replacer.Replace(value)
}

// deriveIngressAndRegistry walks every service section the manifest exposes
// (Services, Interfaces, Observability) and populates:
//   - service_registry rows for non-self-registering public services, keyed off
//     servicedefs for default port / health path / protocol.
//   - ingress.tls_bundles: auto-generated bundles for cluster scopes that need
//     them, plus every entry from manifest.TLSBundles. Auto bundles use the
//     shared env ACME contact email because Quartermaster requires it before
//     reconciling the bundle.
//   - ingress.sites: auto-generated sites for public services, plus every entry
//     from manifest.IngressSites.
//
// Auto entries that collide with explicit manifest entries on the stable id key
// defer to the explicit entry (operator wins over derivation).
func deriveIngressAndRegistry(d *Derived, m *inventory.Manifest, opts DeriveOptions) {
	autoBundles := map[string]TLSBundle{}
	autoSites := map[string]IngressSite{}

	for _, svcMap := range []map[string]inventory.ServiceConfig{m.Services, m.Interfaces, m.Observability} {
		for serviceName, svc := range svcMap {
			if !svc.Enabled {
				continue
			}
			serviceType, isPublic := clusterderive.ManifestServiceType(serviceName, svc)
			registryServiceName, registryServiceType, defName := serviceRegistryIdentity(serviceName, svc, serviceType, isPublic)
			defs, hasDefs := servicedefs.Lookup(defName)
			port := resolveServicePort(svc, defs)
			notSelfRegister := !clusterderive.SelfRegisters(serviceType)

			// One service may be deployed across multiple hosts in different
			// clusters. Resolve cluster id per host so every row carries the
			// host's actual cluster membership (matches how production
			// provisioning resolves clusterID := manifest.HostCluster(task.Host)).
			for _, hostKey := range serviceHostKeys(svc) {
				if hostKey == "" {
					continue
				}
				clusterID := serviceClusterIDForHost(m, serviceName, hostKey)

				if notSelfRegister && isPublic && port != 0 {
					entry := ServiceRegistryEntry{
						ServiceName: registryServiceName,
						Type:        registryServiceType,
						ClusterID:   clusterID,
						NodeID:      hostKey,
						Port:        port,
					}
					if hasDefs {
						entry.Protocol = defs.HealthProtocol
						entry.HealthEndpoint = defs.HealthPath
					}
					if md := deriveServiceMetadata(serviceType, hostKey, port, m, svc, opts); len(md) > 0 {
						entry.Metadata = md
					}
					d.Quartermaster.ServiceRegistry = append(d.Quartermaster.ServiceRegistry, entry)
				}

				// Physical per-instance endpoint: a SEPARATE ingress site +
				// exact-SAN bundle so this node's nginx answers
				// <service>.<node>.infra.<root> (the failover address Foghorn
				// hands MistProcLivepeer) on the same local upstream as the
				// pooled name. Node-scoped, so it does not depend on ingress
				// cluster membership.
				if pkgdns.IsPhysicalEndpointServiceType(serviceType) && port != 0 {
					if fqdn, ok := pkgdns.InfraInstanceFQDN(serviceType, hostKey, m.RootDomain); ok {
						if host, hasHost := m.GetHost(hostKey); hasHost {
							upstreamHost := host.WireguardIP
							if upstreamHost == "" {
								upstreamHost = host.ExternalIP
							}
							if upstreamHost != "" {
								bundleID := clusterderive.TLSBundleID("physical", fqdn)
								upsertAutoTLSBundle(autoBundles, TLSBundle{
									ID:        bundleID,
									ClusterID: clusterID,
									Domains:   []string{fqdn},
									Issuer:    "navigator",
									Email:     resolveTLSBundleEmail(opts),
								})
								siteID := serviceName + "-" + hostKey + "-physical"
								autoSites[siteID] = IngressSite{
									ID:          siteID,
									ClusterID:   clusterID,
									NodeID:      hostKey,
									Domains:     []string{fqdn},
									TLSBundleID: bundleID,
									Kind:        "physical",
									Upstream:    IngressUpstream{Host: upstreamHost, Port: port},
								}
							}
						}
					}
				}

				for _, ingressClusterID := range serviceIngressClusterIDs(serviceName, m, svc, hostKey, clusterID) {
					domains, bundleID := clusterderive.AutoIngressDomainsForService(serviceName, svc, m, ingressClusterID)
					if bundleID == "" || len(domains) == 0 {
						continue
					}

					bundleDomains := domains
					if !strings.HasPrefix(bundleID, "apex-") {
						bundleRoot := clusterderive.PublicServiceRootDomain(serviceType, m, ingressClusterID)
						bundleDomains = clusterderive.WildcardBundleDomains(bundleRoot)
					}
					upsertAutoTLSBundle(autoBundles, TLSBundle{
						ID:        bundleID,
						ClusterID: tlsBundleOwnerClusterID(m, serviceType, ingressClusterID),
						Domains:   bundleDomains,
						Issuer:    "navigator",
						Email:     resolveTLSBundleEmail(opts),
					})

					host, hasHost := m.GetHost(hostKey)
					if !hasHost {
						continue
					}
					upstreamHost := host.WireguardIP
					if upstreamHost == "" {
						upstreamHost = host.ExternalIP
					}
					if upstreamHost == "" || port == 0 {
						continue
					}
					siteID := serviceName + "-" + hostKey
					if ingressClusterID != clusterID {
						siteID += "-" + ingressClusterID
					}
					autoSites[siteID] = IngressSite{
						ID:          siteID,
						ClusterID:   clusterID,
						NodeID:      hostKey,
						Domains:     domains,
						TLSBundleID: bundleID,
						Kind:        "http",
						Upstream:    IngressUpstream{Host: upstreamHost, Port: port},
					}

					globalDomains, globalBundleID := clusterderive.PlatformGlobalRootIngressDomainsForService(serviceName, svc, m, ingressClusterID)
					if len(globalDomains) == 0 || globalBundleID == "" {
						continue
					}
					bundleOwner := platformTLSBundleClusterID(m)
					if bundleOwner == "" {
						bundleOwner = ingressClusterID
					}
					upsertAutoTLSBundle(autoBundles, TLSBundle{
						ID:        globalBundleID,
						ClusterID: bundleOwner,
						Domains:   clusterderive.WildcardBundleDomains(m.RootDomain),
						Issuer:    "navigator",
						Email:     resolveTLSBundleEmail(opts),
					})
					autoSites[serviceName+"-"+hostKey+"-global-root"] = IngressSite{
						ID:          serviceName + "-" + hostKey + "-global-root",
						ClusterID:   clusterID,
						NodeID:      hostKey,
						Domains:     globalDomains,
						TLSBundleID: globalBundleID,
						Kind:        "http",
						Upstream:    IngressUpstream{Host: upstreamHost, Port: port},
					}
				}
			}
		}
	}

	// Explicit manifest TLSBundles win over auto-derived bundles on id collision.
	explicitBundleIDs := map[string]bool{}
	for id, cfg := range m.TLSBundles {
		explicitBundleIDs[id] = true
		d.Quartermaster.Ingress.TLSBundles = append(d.Quartermaster.Ingress.TLSBundles, TLSBundle{
			ID:        id,
			ClusterID: cfg.Cluster,
			Domains:   cfg.Domains,
			Issuer:    cfg.Issuer,
			Email:     cfg.Email,
		})
	}
	for _, b := range autoBundles {
		if explicitBundleIDs[b.ID] {
			continue
		}
		d.Quartermaster.Ingress.TLSBundles = append(d.Quartermaster.Ingress.TLSBundles, b)
	}

	// Explicit manifest IngressSites win over auto-derived sites on id collision.
	explicitSiteIDs := map[string]bool{}
	for id, cfg := range m.IngressSites {
		explicitSiteIDs[id] = true
		host, port := splitHostPort(cfg.Upstream)
		d.Quartermaster.Ingress.Sites = append(d.Quartermaster.Ingress.Sites, IngressSite{
			ID:          id,
			ClusterID:   cfg.Cluster,
			NodeID:      cfg.Node,
			Domains:     cfg.Domains,
			TLSBundleID: cfg.TLSBundleID,
			Kind:        cfg.Kind,
			Upstream:    IngressUpstream{Host: host, Port: port},
		})
	}
	for _, s := range autoSites {
		if explicitSiteIDs[s.ID] {
			continue
		}
		d.Quartermaster.Ingress.Sites = append(d.Quartermaster.Ingress.Sites, s)
	}
}

// resolveServicePort picks the manifest-declared port; falls back to GRPCPort, then
// the servicedef DefaultPort. Zero on no match.
func resolveServicePort(svc inventory.ServiceConfig, defs servicedefs.Service) int {
	if svc.Port != 0 {
		return svc.Port
	}
	if svc.GRPCPort != 0 {
		return svc.GRPCPort
	}
	return defs.DefaultPort
}

func serviceRegistryIdentity(serviceName string, svc inventory.ServiceConfig, serviceType string, isPublic bool) (registryName, registryType, defName string) {
	deployName := strings.TrimSpace(svc.Deploy)
	defName = serviceName
	if deployName != "" {
		defName = deployName
	}
	if isPublic {
		defName = serviceType
	}

	registryName = serviceName
	registryType = serviceType
	if deployName == "vmauth" || (deployName == "" && serviceName == "vmauth") {
		return "vmauth", "vmauth", "vmauth"
	}
	if pkgdns.IsPoolAssignedServiceType(serviceType) {
		registryName = serviceType
	}
	return registryName, registryType, defName
}

// splitHostPort separates "host:port" into pieces. Best-effort — empty string yields
// zero values; unparseable port stays 0.
func splitHostPort(addr string) (string, int) {
	if addr == "" {
		return "", 0
	}
	colon := strings.LastIndex(addr, ":")
	if colon < 0 {
		return addr, 0
	}
	host := addr[:colon]
	var port int
	for _, ch := range addr[colon+1:] {
		if ch < '0' || ch > '9' {
			return host, 0
		}
		port = port*10 + int(ch-'0')
	}
	return host, port
}

// serviceClusterIDForHost resolves the physical cluster id for a service on a
// specific host. service_instances rows must carry the host's runtime cluster
// (FK-bound to the node); logical media-cluster identity is decoupled and
// recorded in service_cluster_assignments by a runtime reconciler.
func serviceClusterIDForHost(m *inventory.Manifest, serviceName, hostKey string) string {
	if hostKey != "" {
		if c := m.HostCluster(hostKey); c != "" {
			return c
		}
	}
	return m.ResolveCluster(serviceName)
}

func serviceIngressClusterIDs(serviceName string, m *inventory.Manifest, svc inventory.ServiceConfig, hostKey, physicalClusterID string) []string {
	if logical, ok := clusterderive.HostScopedLogicalServiceClusterIDs(serviceName, svc, m, hostKey); ok {
		return logical
	}
	if physicalClusterID == "" {
		return nil
	}
	return []string{physicalClusterID}
}

func upsertAutoTLSBundle(bundles map[string]TLSBundle, next TLSBundle) {
	if existing, ok := bundles[next.ID]; ok {
		existing.ClusterID = stableTLSBundleClusterID(existing.ClusterID, next.ClusterID)
		existing.Domains = mergeStringSet(existing.Domains, next.Domains)
		if existing.Issuer == "" {
			existing.Issuer = next.Issuer
		}
		if existing.Email == "" {
			existing.Email = next.Email
		}
		bundles[next.ID] = existing
		return
	}
	next.Domains = mergeStringSet(nil, next.Domains)
	bundles[next.ID] = next
}

func tlsBundleOwnerClusterID(m *inventory.Manifest, serviceType, ingressClusterID string) string {
	if m == nil {
		return ingressClusterID
	}
	root := clusterderive.PublicServiceRootDomain(serviceType, m, ingressClusterID)
	if root != "" && root == strings.TrimSpace(m.RootDomain) {
		if owner := platformTLSBundleClusterID(m); owner != "" {
			return owner
		}
	}
	return ingressClusterID
}

func platformTLSBundleClusterID(m *inventory.Manifest) string {
	if m == nil || len(m.Clusters) == 0 {
		if m != nil {
			return m.ResolveCluster("")
		}
		return ""
	}

	priority := func(c inventory.ClusterConfig) int {
		switch {
		case c.Default && c.Type == "central" && c.PlatformOfficial:
			return 0
		case c.Default && c.Type == "central":
			return 1
		case c.Type == "central" && c.PlatformOfficial:
			return 2
		case c.Type == "central":
			return 3
		case c.Default && c.PlatformOfficial:
			return 4
		case c.Default:
			return 5
		case c.PlatformOfficial:
			return 6
		default:
			return 7
		}
	}

	bestID := ""
	bestPriority := 99
	for clusterID, cluster := range m.Clusters {
		p := priority(cluster)
		if bestID == "" || p < bestPriority || (p == bestPriority && clusterID < bestID) {
			bestID = clusterID
			bestPriority = p
		}
	}
	return bestID
}

func stableTLSBundleClusterID(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	case b < a:
		return b
	default:
		return a
	}
}

func mergeStringSet(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, values := range [][]string{existing, incoming} {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}

// serviceHostKeys returns every host the service is deployed to. Single-host
// services collapse to one entry; multi-host (svc.Hosts) services emit one entry
// per host so derived state matches the per-host registration production runs.
func serviceHostKeys(svc inventory.ServiceConfig) []string {
	if len(svc.Hosts) > 0 {
		return svc.Hosts
	}
	if svc.Host != "" {
		return []string{svc.Host}
	}
	return nil
}

func resolveTLSBundleEmail(opts DeriveOptions) string {
	for _, key := range []string{"TLS_BUNDLE_EMAIL", "ACME_EMAIL"} {
		if v := strings.TrimSpace(opts.SharedEnv[key]); v != "" {
			return v
		}
	}
	return ""
}

// deriveServiceMetadata returns service-specific service_registry metadata
// that the manifest can produce on its own (no on-host runtime data).
//
// livepeer-gateway emits invariant per-instance fields (wallet_address,
// public_scheme, public_port). The cluster-derived public_host is NOT stored
// here: the same gateway pool may serve multiple logical media clusters, so
// the per-cluster URL is synthesized at DiscoverServices/DNS time from the
// requested service_cluster_assignments.cluster_id.
//
// wallet_address is required by Purser's deposit monitor
// (api_billing/internal/handlers/livepeer_deposit.go skips gateways whose
// registry metadata lacks it). Resolution order: service config
// `eth_acct_addr` / `LIVEPEER_ETH_ACCT_ADDR` first, then opts.SharedEnv.
// Validate() fails any livepeer-gateway entry without a resolvable wallet,
// so the gap shows up at render time rather than as a silent skip later.
func deriveServiceMetadata(serviceName, hostKey string, port int, m *inventory.Manifest, svc inventory.ServiceConfig, opts DeriveOptions) map[string]string {
	if serviceName != "livepeer-gateway" {
		return nil
	}
	host, ok := m.GetHost(hostKey)
	if !ok || host.ExternalIP == "" || port == 0 {
		return nil
	}
	md := map[string]string{
		servicedefs.LivepeerGatewayMetadataPublicPort:   "443",
		servicedefs.LivepeerGatewayMetadataPublicScheme: "https",
	}
	if wallet := resolveLivepeerWalletAddress(svc, opts); wallet != "" {
		md[servicedefs.LivepeerGatewayMetadataWalletAddress] = wallet
	}
	return md
}

// resolveLivepeerWalletAddress reads the gateway's Ethereum address. Lookup
// order: the service's manifest config block first (`eth_acct_addr` /
// `LIVEPEER_ETH_ACCT_ADDR`), then opts.SharedEnv. Returns the empty string
// when neither carries a non-empty value; Validate() then fails the rendered
// file when livepeer-gateway is enabled, so the missing wallet shows up at
// render time rather than as a silent skip in Purser's deposit monitor.
func resolveLivepeerWalletAddress(svc inventory.ServiceConfig, opts DeriveOptions) string {
	keys := []string{"eth_acct_addr", "LIVEPEER_ETH_ACCT_ADDR"}
	for _, key := range keys {
		if v := strings.TrimSpace(svc.Config[key]); v != "" {
			return v
		}
	}
	for _, key := range keys {
		if v := strings.TrimSpace(opts.SharedEnv[key]); v != "" {
			return v
		}
	}
	return ""
}

// Render merges the derived layer with the optional overlay and produces the final
// Rendered document with all secrets resolved. The resolver is mandatory whenever the
// merged document carries any non-zero SecretRef; nil resolver is accepted only when
// no PasswordRef appears in the input.
//
// Merge precedence:
//   - Overlay items with new stable keys are appended to the derived list.
//   - Overlay items whose stable key matches a derived item AND carry Override=true
//     update the mutable fields on the derived item; stable fields must match or fail.
//   - Overlay items whose stable key matches without Override=true are a configuration
//     error: fail loud.
func Render(derived *Derived, overlay *Overlay, resolver Resolver) (*Rendered, error) {
	if derived == nil {
		return nil, fmt.Errorf("bootstrap.Render: nil derived")
	}

	r := &Rendered{}

	r.Quartermaster = derived.Quartermaster
	if overlay != nil {
		merged, err := mergeQuartermaster(r.Quartermaster, overlay.Quartermaster)
		if err != nil {
			return nil, fmt.Errorf("merge quartermaster: %w", err)
		}
		r.Quartermaster = merged
	}

	r.Purser = derived.Purser
	if overlay != nil {
		merged, err := mergePurser(r.Purser, overlay.Purser)
		if err != nil {
			return nil, fmt.Errorf("merge purser: %w", err)
		}
		r.Purser = merged
	}

	mergedCommodore := derived.Commodore
	if overlay != nil {
		merged, err := mergeCommodore(mergedCommodore, overlay.Commodore)
		if err != nil {
			return nil, fmt.Errorf("merge commodore: %w", err)
		}
		mergedCommodore = merged
	}
	for i, ps := range mergedCommodore.PullStreams {
		rps, err := pullStreamToRendered(ps, resolver, r.Quartermaster.Clusters)
		if err != nil {
			return nil, fmt.Errorf("commodore.pull_streams[%d] (%s): %w", i, ps.PlaybackID, err)
		}
		r.Commodore.PullStreams = append(r.Commodore.PullStreams, rps)
	}
	for i, ms := range mergedCommodore.MistNativeStreams {
		rms, err := mistNativeStreamToRendered(ms, r.Quartermaster.Clusters)
		if err != nil {
			return nil, fmt.Errorf("commodore.mist_native_streams[%d] (%s): %w", i, ms.PlaybackID, err)
		}
		r.Commodore.MistNativeStreams = append(r.Commodore.MistNativeStreams, rms)
	}

	mergedAccounts := derived.Accounts
	if overlay != nil {
		mergedAccounts = mergeAccounts(mergedAccounts, overlay.Accounts)
	}
	for i, a := range mergedAccounts {
		ra, err := accountToRendered(a, resolver)
		if err != nil {
			return nil, fmt.Errorf("accounts[%d] (%s): %w", i, a.Tenant.Ref, err)
		}
		r.Accounts = append(r.Accounts, ra)
	}

	return r, nil
}

// === merge helpers ===

func mergeQuartermaster(derived, overlay QuartermasterSection) (QuartermasterSection, error) {
	out := derived

	// SystemTenant: overlay can update fields when its alias matches the derived
	// alias. Mismatched aliases are a configuration error — the system tenant alias
	// is fixed at SystemTenantAlias.
	if overlay.SystemTenant != nil {
		alias := overlay.SystemTenant.Alias
		if alias == "" {
			alias = SystemTenantAlias
		}
		if out.SystemTenant == nil {
			out.SystemTenant = overlay.SystemTenant
			out.SystemTenant.Alias = alias
		} else if out.SystemTenant.Alias == alias {
			out.SystemTenant = overlay.SystemTenant
			out.SystemTenant.Alias = alias
		} else {
			return out, fmt.Errorf("system_tenant alias mismatch: derived=%q overlay=%q",
				out.SystemTenant.Alias, alias)
		}
	}

	// Tenants: additive by id; overlay-only since derived emits none today.
	out.Tenants = mergeTenants(out.Tenants, overlay.Tenants)

	// Clusters: additive by id, override on Override=true.
	merged, err := mergeClusters(out.Clusters, overlay.Clusters)
	if err != nil {
		return out, err
	}
	out.Clusters = merged

	// Nodes: overlay nodes are additive (rare; usually all from manifest). Same-id
	// collision without an explicit override is a configuration error.
	mergedNodes, err := mergeNodes(out.Nodes, overlay.Nodes)
	if err != nil {
		return out, err
	}
	out.Nodes = mergedNodes

	// TLSBundles, Sites, ServiceRegistry: additive by stable key. Per-id
	// collisions are flagged in Validate().
	out.Ingress.TLSBundles = append(out.Ingress.TLSBundles, overlay.Ingress.TLSBundles...)
	out.Ingress.Sites = append(out.Ingress.Sites, overlay.Ingress.Sites...)
	out.ServiceRegistry = append(out.ServiceRegistry, overlay.ServiceRegistry...)

	// SystemTenantClusterAccess: overlay overrides if present.
	if overlay.SystemTenantClusterAccess != nil {
		out.SystemTenantClusterAccess = overlay.SystemTenantClusterAccess
	}

	// Mesh: overlay overrides if present.
	if overlay.Mesh != nil {
		out.Mesh = overlay.Mesh
	}

	return out, nil
}

func mergePurser(derived, overlay PurserSection) (PurserSection, error) {
	out := derived

	pricings, err := mergeClusterPricings(out.ClusterPricing, overlay.ClusterPricing)
	if err != nil {
		return out, err
	}
	out.ClusterPricing = pricings

	// CustomerBilling: additive by tenant_id. Same-tenant collisions surface as a
	// duplicate-tenant error in Validate().
	out.CustomerBilling = append(out.CustomerBilling, overlay.CustomerBilling...)

	// BillingTiers: pure additive (the embedded catalog is layer 2, owned by the
	// Purser binary; this slot is the overlay-only contribution). Per-id collisions
	// inside this slot are caught in Validate(). Field-level merge against the
	// embedded catalog happens inside `purser bootstrap`, not here.
	out.BillingTiers = append(out.BillingTiers, overlay.BillingTiers...)

	return out, nil
}

func mergeClusterPricings(derived, overlay []ClusterPricing) ([]ClusterPricing, error) {
	out := append([]ClusterPricing(nil), derived...)
	for _, op := range overlay {
		idx := indexClusterPricingByClusterID(out, op.ClusterID)
		switch {
		case idx == -1:
			op.Override = false
			out = append(out, op)
		case op.Override:
			out[idx] = mergeClusterPricingFields(out[idx], op)
		default:
			return nil, fmt.Errorf("cluster_pricing for cluster %q: overlay collides with derived; set override: true to replace", op.ClusterID)
		}
	}
	return out, nil
}

// mergeClusterPricingFields applies overlay over derived field-by-field. An overlay
// field is "set" when it differs from the type's zero value; unset fields keep the
// derived value. Pointer fields (RequiredTierLevel, AllowFreeTier) and maps
// (MeteredRates, DefaultQuotas) follow the same rule: nil overlay → keep derived.
func mergeClusterPricingFields(derived, overlay ClusterPricing) ClusterPricing {
	out := derived
	if overlay.PricingModel != "" {
		out.PricingModel = overlay.PricingModel
	}
	if overlay.RequiredTierLevel != nil {
		out.RequiredTierLevel = overlay.RequiredTierLevel
	}
	if overlay.AllowFreeTier != nil {
		out.AllowFreeTier = overlay.AllowFreeTier
	}
	if overlay.BasePrice != "" {
		out.BasePrice = overlay.BasePrice
	}
	if overlay.Currency != "" {
		out.Currency = overlay.Currency
	}
	if overlay.MeteredRates != nil {
		out.MeteredRates = overlay.MeteredRates
	}
	if overlay.DefaultQuotas != nil {
		out.DefaultQuotas = overlay.DefaultQuotas
	}
	out.Override = false
	return out
}

func indexClusterPricingByClusterID(ps []ClusterPricing, id string) int {
	for i, p := range ps {
		if p.ClusterID == id {
			return i
		}
	}
	return -1
}

func mergeTenants(derived, overlay []Tenant) []Tenant {
	out := append([]Tenant(nil), derived...)
	out = append(out, overlay...)
	return out
}

func mergeClusters(derived, overlay []Cluster) ([]Cluster, error) {
	out := append([]Cluster(nil), derived...)
	for _, oc := range overlay {
		idx := indexClusterByID(out, oc.ID)
		switch {
		case idx == -1:
			oc.Override = false
			out = append(out, oc)
		case oc.Override:
			merged, err := mergeClusterFields(out[idx], oc)
			if err != nil {
				return nil, err
			}
			merged.Override = false
			out[idx] = merged
		default:
			return nil, fmt.Errorf("cluster %q: overlay collides with derived; set override: true to replace", oc.ID)
		}
	}
	return out, nil
}

// mergeClusterFields enforces the documented stable/mutable split. Stable fields must
// match between derived and overlay or fail loud — Override = true is permission to
// modify mutable fields, never permission to silently rewrite stable ones (id,
// owner_tenant, mesh.cidr).
func mergeClusterFields(derived, overlay Cluster) (Cluster, error) {
	if !overlay.OwnerTenant.IsZero() && overlay.OwnerTenant.Ref != derived.OwnerTenant.Ref {
		return derived, fmt.Errorf("cluster %q: owner_tenant is stable (derived=%q overlay=%q); cluster ownership change requires explicit re-provision",
			derived.ID, derived.OwnerTenant.Ref, overlay.OwnerTenant.Ref)
	}
	if overlay.Mesh.CIDR != "" && overlay.Mesh.CIDR != derived.Mesh.CIDR {
		return derived, fmt.Errorf("cluster %q: mesh.cidr is stable (derived=%q overlay=%q); CIDR change is a re-provision, not an override",
			derived.ID, derived.Mesh.CIDR, overlay.Mesh.CIDR)
	}

	out := derived
	if overlay.Name != "" {
		out.Name = overlay.Name
	}
	if overlay.Type != "" {
		out.Type = overlay.Type
	}
	if overlay.Region != "" {
		out.Region = overlay.Region
	}
	if overlay.BaseURL != "" {
		out.BaseURL = overlay.BaseURL
	}
	out.IsDefault = overlay.IsDefault
	out.IsPlatformOfficial = overlay.IsPlatformOfficial
	out.PublicTopology = overlay.PublicTopology
	out.AllowPrivatePullSources = overlay.AllowPrivatePullSources
	if overlay.Cell != "" {
		out.Cell = overlay.Cell
	}
	if overlay.Class != "" {
		out.Class = overlay.Class
	}
	if overlay.ControlCell != "" {
		out.ControlCell = overlay.ControlCell
	}
	if len(overlay.EligibleServingCells) > 0 {
		out.EligibleServingCells = overlay.EligibleServingCells
	}
	if overlay.S3Bucket != "" {
		out.S3Bucket = overlay.S3Bucket
	}
	if overlay.S3Endpoint != "" {
		out.S3Endpoint = overlay.S3Endpoint
	}
	if overlay.S3Region != "" {
		out.S3Region = overlay.S3Region
	}
	if overlay.Mesh.ListenPort != 0 {
		out.Mesh.ListenPort = overlay.Mesh.ListenPort
	}
	return out, nil
}

func mergeNodes(derived, overlay []Node) ([]Node, error) {
	out := append([]Node(nil), derived...)
	for _, on := range overlay {
		if indexNodeByID(out, on.ID) != -1 {
			return nil, fmt.Errorf("node %q: overlay collides with derived; node identity is stable, refusing silent replacement", on.ID)
		}
		out = append(out, on)
	}
	return out, nil
}

func mergeAccounts(derived, overlay []AccountDerived) []AccountDerived {
	out := append([]AccountDerived(nil), derived...)
	out = append(out, overlay...)
	return out
}

// mergeCommodore overlays operator-owned pull streams and mist-native streams.
// Stable key: PlaybackID. Additive when keys differ; Override=true updates
// mutable fields; collision without Override is a configuration error.
func mergeCommodore(derived, overlay CommodoreSection) (CommodoreSection, error) {
	out := derived
	for _, ops := range overlay.PullStreams {
		idx := indexPullStreamByPlaybackID(out.PullStreams, ops.PlaybackID)
		switch {
		case idx == -1:
			ops.Override = false
			out.PullStreams = append(out.PullStreams, ops)
		case ops.Override:
			ops.Override = false
			out.PullStreams[idx] = ops
		default:
			return out, fmt.Errorf("pull_stream %q: overlay collides with derived without override=true", ops.PlaybackID)
		}
	}
	for _, oms := range overlay.MistNativeStreams {
		idx := indexMistNativeByPlaybackID(out.MistNativeStreams, oms.PlaybackID)
		switch {
		case idx == -1:
			oms.Override = false
			out.MistNativeStreams = append(out.MistNativeStreams, oms)
		case oms.Override:
			oms.Override = false
			out.MistNativeStreams[idx] = oms
		default:
			return out, fmt.Errorf("mist_native_stream %q: overlay collides with derived without override=true", oms.PlaybackID)
		}
	}
	return out, nil
}

func indexPullStreamByPlaybackID(ps []PullStream, id string) int {
	for i, p := range ps {
		if p.PlaybackID == id {
			return i
		}
	}
	return -1
}

func indexMistNativeByPlaybackID(ms []MistNativeStream, id string) int {
	for i, m := range ms {
		if m.PlaybackID == id {
			return i
		}
	}
	return -1
}

// pullStreamToRendered resolves SourceURIRef → plaintext SourceURI and
// validates URI shape + cluster eligibility against the manifest's rendered
// media clusters. The manifest is the source of truth at render time;
// Quartermaster may not exist yet or may be mid-reconcile, so we don't dial
// it here — eligibility runs against the same cluster definitions the
// reconciler will apply.
//
// Private/multicast URI literals require explicit allowed_cluster_ids, and
// every listed cluster must be a media cluster with allow_private_pull_sources.
// Public URIs can omit allowed_cluster_ids to run on any media cluster, or set
// it to pin placement.
func pullStreamToRendered(p PullStream, resolver Resolver, clusters []Cluster) (PullStreamRendered, error) {
	uri := p.SourceURI
	hasInline := uri != ""
	hasRef := !p.SourceURIRef.IsZero()
	switch {
	case hasInline && hasRef:
		return PullStreamRendered{}, fmt.Errorf("source_uri and source_uri_ref are mutually exclusive")
	case !hasInline && !hasRef:
		return PullStreamRendered{}, fmt.Errorf("one of source_uri / source_uri_ref must be set")
	case hasRef:
		if resolver == nil {
			return PullStreamRendered{}, fmt.Errorf("source_uri_ref present but no resolver supplied")
		}
		v, err := resolver.Resolve(p.SourceURIRef)
		if err != nil {
			return PullStreamRendered{}, fmt.Errorf("resolve source_uri_ref: %w", err)
		}
		uri = v
	}
	if p.PlaybackID == "" {
		return PullStreamRendered{}, fmt.Errorf("playback_id is required")
	}
	if p.OwnerTenant.IsZero() {
		return PullStreamRendered{}, fmt.Errorf("owner_tenant ref is required")
	}

	class, classErr := pullsource.Classify(uri)
	if class == pullsource.ClassBlocked {
		return PullStreamRendered{}, fmt.Errorf("source_uri: %w", classErr)
	}
	candidates := mediaClusterCapabilities(clusters)
	if len(candidates) == 0 {
		return PullStreamRendered{}, fmt.Errorf("no media (edge) cluster is registered to host pull streams")
	}

	allowedIDs := normalizeAllowedClusterIDs(p.AllowedClusterIDs)
	eligible, rejects := pullsource.FilterPlacementClusters(class, allowedIDs, candidates)
	if err := formatPlacementRejects(p.PlaybackID, pullsource.Redact(uri), rejects); err != nil {
		return PullStreamRendered{}, err
	}
	if len(eligible) == 0 {
		// Defensive: FilterPlacementClusters already emits a reject in every
		// no-eligible case, so we should never reach here. Fail closed if we do.
		return PullStreamRendered{}, fmt.Errorf("pull_stream %q: no eligible media cluster", p.PlaybackID)
	}

	return PullStreamRendered{
		PlaybackID:        p.PlaybackID,
		OwnerTenant:       p.OwnerTenant,
		Title:             p.Title,
		Description:       p.Description,
		SourceURI:         uri,
		Enabled:           p.Enabled,
		AllowedClusterIDs: allowedIDs,
	}, nil
}

// mistNativeStreamToRendered validates an operator-owned Mist-native stream
// declaration. Concrete Mist configs (literal source, always_on) bypass
// PUSH_REWRITE entirely; Foghorn picks an eligible edge on its managed-stream
// reconciler tick. Validation: source_kind must match the source prefix, every
// Mist-native stream requires the operator/system tenant (any source_kind), and
// AllowedClusterIDs must contain exactly one declared media cluster.
func mistNativeStreamToRendered(m MistNativeStream, clusters []Cluster) (MistNativeStreamRendered, error) {
	if m.PlaybackID == "" {
		return MistNativeStreamRendered{}, fmt.Errorf("playback_id is required")
	}
	if m.OwnerTenant.IsZero() {
		return MistNativeStreamRendered{}, fmt.Errorf("owner_tenant ref is required")
	}
	if m.Source == "" {
		return MistNativeStreamRendered{}, fmt.Errorf("source is required")
	}
	if err := validateMistNativeSourceKind(m.SourceKind, m.Source); err != nil {
		return MistNativeStreamRendered{}, err
	}
	// mist_native is operator-tenant-only for every source_kind: customer-
	// owned managed streams would bypass the free-tier-load and per-tenant
	// stream-cap gates that PUSH_REWRITE enforces. The managed-stream
	// reconciler admits on suspension and balance only.
	if !isSystemTenantRef(m.OwnerTenant) {
		return MistNativeStreamRendered{}, fmt.Errorf("mist_native streams require owner_tenant=%s; tenant-supplied managed streams are not enabled", SystemTenantAlias)
	}

	count := m.PlacementCount
	if count == 0 {
		count = 1
	}
	if count < 1 {
		return MistNativeStreamRendered{}, fmt.Errorf("placement_count must be >= 1, got %d", count)
	}

	allowedIDs := normalizeAllowedClusterIDs(m.AllowedClusterIDs)
	// allowed_cluster_ids currently names exactly one source cluster the stream
	// may run on. Foghorn elects an edge inside that cluster; the elected
	// edge's cluster becomes active_ingest_cluster_id. Federation handles
	// cross-cluster viewer routing from that single active source.
	if len(allowedIDs) == 0 {
		return MistNativeStreamRendered{}, fmt.Errorf("allowed_cluster_ids must contain at least one media cluster")
	}
	mediaIDs := mediaClusterIDSet(clusters)
	for _, id := range allowedIDs {
		if !mediaIDs[id] {
			return MistNativeStreamRendered{}, fmt.Errorf("allowed_cluster_ids: cluster %q is not a declared media cluster", id)
		}
	}
	if len(allowedIDs) != 1 {
		return MistNativeStreamRendered{}, fmt.Errorf("allowed_cluster_ids currently supports exactly one source cluster for mist_native streams (got %d); cross-cluster source election is not implemented", len(allowedIDs))
	}

	return MistNativeStreamRendered{
		PlaybackID:         m.PlaybackID,
		OwnerTenant:        m.OwnerTenant,
		Title:              m.Title,
		Description:        m.Description,
		Source:             m.Source,
		SourceKind:         m.SourceKind,
		AlwaysOn:           m.AlwaysOn,
		IsRecordingEnabled: m.IsRecordingEnabled,
		ProcessPolicy:      m.ProcessPolicy,
		PlacementCount:     count,
		AllowedClusterIDs:  allowedIDs,
		LocalAssets:        m.LocalAssets,
	}, nil
}

func validateMistNativeSourceKind(kind, source string) error {
	switch kind {
	case "exec":
		if !strings.HasPrefix(source, "ts-exec:") {
			return fmt.Errorf("source_kind=exec requires source to start with 'ts-exec:'")
		}
	case "file":
		if !strings.HasPrefix(source, "file://") && !strings.HasPrefix(source, "/") {
			return fmt.Errorf("source_kind=file requires source to start with 'file://' or '/'")
		}
	case "playlist":
		if !strings.HasPrefix(source, "playlist:") &&
			!strings.HasSuffix(source, ".pls") &&
			!strings.HasSuffix(source, ".m3u") &&
			!strings.HasSuffix(source, ".m3u8") {
			return fmt.Errorf("source_kind=playlist requires source prefix 'playlist:' or a .pls/.m3u/.m3u8 path")
		}
	case "":
		return fmt.Errorf("source_kind is required (file | playlist | exec)")
	default:
		return fmt.Errorf("source_kind %q is not supported (file | playlist | exec)", kind)
	}
	return nil
}

func isSystemTenantRef(ref TenantRef) bool {
	// TenantRef.Ref is a path like "quartermaster.system_tenant" or
	// "quartermaster.tenants.<alias>". The system tenant lives at a stable
	// path; resolution against actual rows happens at reconcile time. Here
	// we accept either form.
	r := strings.TrimSpace(ref.Ref)
	return r == "quartermaster.system_tenant" ||
		r == "quartermaster.tenants."+SystemTenantAlias
}

func mediaClusterIDSet(clusters []Cluster) map[string]bool {
	out := make(map[string]bool, len(clusters))
	for _, c := range clusters {
		// Type=="edge" marks media-capable clusters (matches
		// mediaClusterCapabilities convention); central clusters host
		// control plane only.
		if c.Type == "edge" {
			out[c.ID] = true
		}
	}
	return out
}

// normalizeAllowedClusterIDs dedups, drops empties, and sorts so the rendered
// file, the persisted TEXT[], and idempotent reconcile compares are stable.
func normalizeAllowedClusterIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// formatPlacementRejects turns pullsource.PlacementReject entries into a
// single render-time error. Render is offline; we batch every reject into one
// message so operators see the full set in a single CLI pass.
func formatPlacementRejects(playbackID, redactedURI string, rejects []pullsource.PlacementReject) error {
	if len(rejects) == 0 {
		return nil
	}
	parts := make([]string, 0, len(rejects))
	for _, r := range rejects {
		switch r.Reason {
		case pullsource.PlacementRejectEmptyForPrivate:
			parts = append(parts, fmt.Sprintf(
				"source_uri %s is private/multicast and requires explicit allowed_cluster_ids", redactedURI))
		case pullsource.PlacementRejectUnknownCluster:
			parts = append(parts, fmt.Sprintf(
				"allowed_cluster_ids entry %q is not a registered media (edge) cluster", r.ClusterID))
		case pullsource.PlacementRejectMissingPrivateCapability:
			parts = append(parts, fmt.Sprintf(
				"allowed_cluster_ids entry %q does not have allow_private_pull_sources=true", r.ClusterID))
		default:
			parts = append(parts, fmt.Sprintf("allowed_cluster_ids entry %q rejected: %s", r.ClusterID, r.Reason))
		}
	}
	return fmt.Errorf("pull_stream %q: %s", playbackID, strings.Join(parts, "; "))
}

// mediaClusterCapabilities maps the manifest's media-capable clusters to the
// shape the eligibility helper consumes. "edge" type means media-capable in
// this codebase; central clusters host control plane only.
func mediaClusterCapabilities(clusters []Cluster) []pullsource.ClusterCapability {
	out := make([]pullsource.ClusterCapability, 0, len(clusters))
	for _, c := range clusters {
		if c.Type != "edge" {
			continue
		}
		out = append(out, pullsource.ClusterCapability{
			ID:                      c.ID,
			AllowPrivatePullSources: c.AllowPrivatePullSources,
		})
	}
	return out
}

func accountToRendered(a AccountDerived, resolver Resolver) (AccountRendered, error) {
	users := make([]AccountUserRendered, 0, len(a.Users))
	for j, u := range a.Users {
		ru := AccountUserRendered{AccountUserCommon: u.AccountUserCommon}
		if !u.PasswordRef.IsZero() {
			if resolver == nil {
				return AccountRendered{}, fmt.Errorf("users[%d] %q: password_ref present but no resolver supplied", j, u.Email)
			}
			pw, err := resolver.Resolve(u.PasswordRef)
			if err != nil {
				return AccountRendered{}, fmt.Errorf("users[%d] %q: resolve password_ref: %w", j, u.Email, err)
			}
			ru.Password = pw
		}
		users = append(users, ru)
	}
	return AccountRendered{
		Kind:    a.Kind,
		Tenant:  a.Tenant,
		Users:   users,
		Billing: a.Billing,
	}, nil
}

// === small utilities ===

func indexClusterByID(cs []Cluster, id string) int {
	for i, c := range cs {
		if c.ID == id {
			return i
		}
	}
	return -1
}

func indexNodeByID(ns []Node, id string) int {
	for i, n := range ns {
		if n.ID == id {
			return i
		}
	}
	return -1
}

// deriveOwnerTenantRef maps the manifest's owner_tenant declaration into a TenantRef.
// Empty or "frameworks" → system tenant ref. Anything else is a customer-tenant alias
// supplied by the manifest/overlay author.
func deriveOwnerTenantRef(declared string) TenantRef {
	if declared == "" || declared == SystemTenantAlias {
		return TenantRefSystem()
	}
	return TenantRefAlias(declared)
}

func deriveClusterBaseURL(m *inventory.Manifest) string {
	if m.RootDomain != "" {
		return "https://" + m.RootDomain
	}
	return ""
}

func int32PtrFromIntPtr(p *int) *int32 {
	if p == nil {
		return nil
	}
	v := int32(*p)
	return &v
}

func intMapToAny(in map[string]int) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
