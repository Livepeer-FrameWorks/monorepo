package bootstrap

import (
	"fmt"
	"slices"
	"strings"

	"frameworks/cli/pkg/clusterderive"
	"frameworks/cli/pkg/inventory"
	pkgdns "frameworks/pkg/dns"
	"frameworks/pkg/servicedefs"
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

	d.Quartermaster.SystemTenant = &Tenant{
		Alias:          SystemTenantAlias,
		Name:           "FrameWorks",
		DeploymentTier: "global",
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
		}
		if c.Type == "" {
			c.Type = m.Type
		}
		if c.Type == "" {
			c.Type = "central"
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
	type bundleKey struct{ clusterID, bundleID string }
	autoBundles := map[bundleKey]TLSBundle{}
	autoSites := map[string]IngressSite{}

	for _, svcMap := range []map[string]inventory.ServiceConfig{m.Services, m.Interfaces, m.Observability} {
		for serviceName, svc := range svcMap {
			if !svc.Enabled {
				continue
			}
			defs, hasDefs := servicedefs.Lookup(serviceName)
			port := resolveServicePort(svc, defs)
			serviceType, isPublic := clusterderive.PublicServiceType(serviceName)
			notSelfRegister := !clusterderive.SelfRegisters(serviceName)

			// One service may be deployed across multiple hosts in different
			// clusters. Resolve cluster id per host so every row carries the
			// host's actual cluster membership (matches how production
			// provisioning resolves clusterID := manifest.HostCluster(task.Host)).
			for _, hostKey := range serviceHostKeys(svc) {
				if hostKey == "" {
					continue
				}
				clusterID := serviceClusterIDForHost(m, serviceName, hostKey, svc)

				if notSelfRegister && isPublic && port != 0 {
					entry := ServiceRegistryEntry{
						ServiceName: serviceName,
						Type:        serviceType,
						ClusterID:   clusterID,
						NodeID:      hostKey,
						Port:        port,
					}
					if hasDefs {
						entry.Protocol = defs.HealthProtocol
						entry.HealthEndpoint = defs.HealthPath
					}
					if md := deriveServiceMetadata(serviceName, hostKey, clusterID, port, m, svc, opts); len(md) > 0 {
						entry.Metadata = md
					}
					d.Quartermaster.ServiceRegistry = append(d.Quartermaster.ServiceRegistry, entry)
				}

				domains, bundleID := clusterderive.AutoIngressDomains(serviceName, m, clusterID)
				if bundleID == "" || len(domains) == 0 {
					continue
				}

				key := bundleKey{clusterID: clusterID, bundleID: bundleID}
				if _, exists := autoBundles[key]; !exists {
					bundleDomains := domains
					if !strings.HasPrefix(bundleID, "apex-") {
						bundleRoot := clusterderive.PublicServiceRootDomain(serviceType, m, clusterID)
						bundleDomains = clusterderive.WildcardBundleDomains(bundleRoot)
					}
					autoBundles[key] = TLSBundle{
						ID:        bundleID,
						ClusterID: clusterID,
						Domains:   bundleDomains,
						Issuer:    "navigator",
						Email:     resolveTLSBundleEmail(opts),
					}
				}

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
				autoSites[siteID] = IngressSite{
					ID:          siteID,
					ClusterID:   clusterID,
					NodeID:      hostKey,
					Domains:     domains,
					TLSBundleID: bundleID,
					Kind:        "http",
					Upstream:    IngressUpstream{Host: upstreamHost, Port: port},
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

// serviceClusterIDForHost resolves the cluster id for a service on a specific host.
// svc.Cluster wins (explicit pin); otherwise the host's cluster membership decides;
// otherwise the service-name fallback. Per-host because a service deployed across
// hosts in different clusters must produce one cluster-correct row per host.
func serviceClusterIDForHost(m *inventory.Manifest, serviceName, hostKey string, svc inventory.ServiceConfig) string {
	if svc.Cluster != "" {
		return svc.Cluster
	}
	if hostKey != "" {
		if c := m.HostCluster(hostKey); c != "" {
			return c
		}
	}
	return m.ResolveCluster(serviceName)
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
// livepeer-gateway emits:
//   - public_host: the gateway's published hostname. Resolution order:
//     service `config.gateway_host` (manifest authority), then
//     `gateway_host` / `LIVEPEER_GATEWAY_HOST` from shared env, then the
//     cluster-scoped FQDN (livepeer.<cluster-slug>.<root-domain>) when the
//     manifest has a root domain, then the root-domain FQDN, then the
//     host's external IP as a last resort. api_balancing uses this to
//     build the gateway URL for media routing, so the IP fallback is
//     correct only when no DNS is configured.
//   - public_port: the manifest service port.
//   - wallet_address: required by Purser's deposit monitor
//     (api_billing/internal/handlers/livepeer_deposit.go skips gateways
//     whose registry metadata lacks it). Resolution order: service config
//     `eth_acct_addr` / `LIVEPEER_ETH_ACCT_ADDR` first, then opts.SharedEnv
//     (production gitops carries `LIVEPEER_ETH_ACCT_ADDR` in config/
//     production.env). Validate() fails any livepeer-gateway entry without
//     a resolvable wallet, so the gap shows up at render time.
//
// Admin endpoints (admin_host / admin_port) are intentionally NOT modeled —
// the gateway's CLI port is container-local in docker mode, so admin access
// uses operator transport (SSH tunnel, ansible-local exec, docker exec), not
// service discovery.
func deriveServiceMetadata(serviceName, hostKey, clusterID string, port int, m *inventory.Manifest, svc inventory.ServiceConfig, opts DeriveOptions) map[string]string {
	if serviceName != "livepeer-gateway" {
		return nil
	}
	host, ok := m.GetHost(hostKey)
	if !ok || host.ExternalIP == "" || port == 0 {
		return nil
	}
	publicHost := resolveLivepeerPublicHost(serviceName, svc, opts, m, clusterID, host.ExternalIP)
	md := map[string]string{
		servicedefs.LivepeerGatewayMetadataPublicHost: publicHost,
		servicedefs.LivepeerGatewayMetadataPublicPort: strconvI(port),
	}
	if wallet := resolveLivepeerWalletAddress(svc, opts); wallet != "" {
		md[servicedefs.LivepeerGatewayMetadataWalletAddress] = wallet
	}
	return md
}

// resolveLivepeerPublicHost picks the gateway's published hostname. Service
// config / shared-env override wins; otherwise the cluster-scoped FQDN, then
// the root-domain FQDN, then the host's external IP.
func resolveLivepeerPublicHost(serviceName string, svc inventory.ServiceConfig, opts DeriveOptions, m *inventory.Manifest, clusterID, externalIP string) string {
	if v := strings.TrimSpace(svc.Config["gateway_host"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(opts.SharedEnv["gateway_host"]); v != "" {
		return v
	}
	if v := strings.TrimSpace(opts.SharedEnv["LIVEPEER_GATEWAY_HOST"]); v != "" {
		return v
	}
	if scope := clusterderive.ClusterScopedRootDomain(m, clusterID); scope != "" {
		if fqdn, ok := pkgdns.ServiceFQDN(serviceName, scope); ok {
			return fqdn
		}
	}
	if m != nil && m.RootDomain != "" {
		if fqdn, ok := pkgdns.ServiceFQDN(serviceName, m.RootDomain); ok {
			return fqdn
		}
	}
	return externalIP
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

// strconvI is a minimal int→string helper for the metadata map; avoids a strconv
// import where this file otherwise has none.
func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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
