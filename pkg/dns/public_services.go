package dns

import (
	"net/url"
	"slices"
	"strings"
)

type Provider string

const (
	ProviderNone       Provider = "none"
	ProviderCloudflare Provider = "cloudflare"
	ProviderBunny      Provider = "bunny"
)

var managedServiceTypes = []string{
	"edge",
	"edge-egress",
	"edge-ingest",
	"edge-storage",
	"edge-processing",
	"telemetry",
	"chandler",
	"foghorn",
	"livepeer-gateway",
	"bridge",
	"chartroom",
	"foredeck",
	"logbook",
	"steward",
	"listmonk",
	"chatwoot",
	"grafana",
	"metabase",
}

var serviceProviders = map[string]Provider{
	"edge":             ProviderBunny,
	"edge-egress":      ProviderBunny,
	"edge-ingest":      ProviderBunny,
	"edge-storage":     ProviderBunny,
	"edge-processing":  ProviderBunny,
	"telemetry":        ProviderBunny,
	"chandler":         ProviderBunny,
	"foghorn":          ProviderBunny,
	"livepeer-gateway": ProviderBunny,
	"bridge":           ProviderCloudflare,
	"chartroom":        ProviderCloudflare,
	"foredeck":         ProviderCloudflare,
	"logbook":          ProviderCloudflare,
	"steward":          ProviderCloudflare,
	"listmonk":         ProviderCloudflare,
	"chatwoot":         ProviderCloudflare,
	"grafana":          ProviderCloudflare,
	"metabase":         ProviderCloudflare,
}

var clusterScopedServiceTypes = map[string]struct{}{
	"chandler":         {},
	"edge":             {},
	"edge-egress":      {},
	"edge-ingest":      {},
	"edge-storage":     {},
	"edge-processing":  {},
	"telemetry":        {},
	"foghorn":          {},
	"livepeer-gateway": {},
}

var poolAssignedServiceTypes = []string{
	"foghorn",
	"chandler",
	"livepeer-gateway",
	"vmauth",
}

var poolAssignedServiceTypeSet = map[string]struct{}{
	"foghorn":          {},
	"chandler":         {},
	"livepeer-gateway": {},
	"vmauth":           {},
}

const TenantAliasZoneLabel = "cdn"

var publicSubdomains = map[string]string{
	"chandler":         "chandler",
	"edge":             "edge",
	"edge-egress":      "edge-egress",
	"edge-ingest":      "edge-ingest",
	"edge-storage":     "edge-storage",
	"edge-processing":  "edge-processing",
	"telemetry":        "telemetry",
	"foghorn":          "foghorn",
	"livepeer-gateway": "livepeer",
	"bridge":           "bridge",
	"chartroom":        "chartroom",
	"foredeck":         "",
	"logbook":          "logbook",
	"steward":          "steward",
	"listmonk":         "listmonk",
	"chatwoot":         "chatwoot",
	"grafana":          "grafana",
	"metabase":         "metabase",
}

// ManagedServiceTypes returns the public service types Navigator reconciles.
func ManagedServiceTypes() []string {
	out := make([]string, len(managedServiceTypes))
	copy(out, managedServiceTypes)
	return out
}

// ProviderForServiceType returns the DNS provider responsible for a public
// service type. Unknown services are not publicly managed.
func ProviderForServiceType(serviceType string) Provider {
	if provider, ok := serviceProviders[serviceType]; ok {
		return provider
	}
	return ProviderNone
}

// BunnyManagedServiceTypes returns service types reconciled under delegated
// media cluster zones in Bunny DNS.
func BunnyManagedServiceTypes() []string {
	return managedServiceTypesForProvider(ProviderBunny)
}

// CloudflareManagedServiceTypes returns root/public service types reconciled in
// Cloudflare DNS.
func CloudflareManagedServiceTypes() []string {
	return managedServiceTypesForProvider(ProviderCloudflare)
}

func managedServiceTypesForProvider(provider Provider) []string {
	out := make([]string, 0, len(managedServiceTypes))
	for _, serviceType := range managedServiceTypes {
		if ProviderForServiceType(serviceType) == provider {
			out = append(out, serviceType)
		}
	}
	return out
}

// IsClusterScopedServiceType reports whether Navigator should also reconcile
// per-cluster records for the given service type.
func IsClusterScopedServiceType(serviceType string) bool {
	_, ok := clusterScopedServiceTypes[serviceType]
	return ok
}

// PoolAssignedServiceTypes returns service types whose public media-cluster
// identity is carried by Quartermaster service_cluster_assignments.
func PoolAssignedServiceTypes() []string {
	out := make([]string, len(poolAssignedServiceTypes))
	copy(out, poolAssignedServiceTypes)
	return out
}

// IsPoolAssignedServiceType reports whether DNS and discovery must resolve the
// service's logical cluster through service_cluster_assignments.
func IsPoolAssignedServiceType(serviceType string) bool {
	_, ok := poolAssignedServiceTypeSet[serviceType]
	return ok
}

// UsesBunnyClusterDNS reports whether a cluster type owns Bunny-managed media
// DNS zones.
func UsesBunnyClusterDNS(clusterType string) bool {
	return clusterType == "edge"
}

// PublicSubdomain maps an internal service type to its public DNS label.
// The empty string represents the zone apex.
func PublicSubdomain(serviceType string) (string, bool) {
	subdomain, ok := publicSubdomains[serviceType]
	return subdomain, ok
}

// RootServiceFQDN resolves the Cloudflare-served root operator services
// (bridge, grafana, listmonk, ...). Bunny-managed services return false
// because they are reconciled in per-service Bunny zones, not in
// Cloudflare. Use BunnyRootServiceFQDN for those.
func RootServiceFQDN(serviceType, rootDomain string) (string, bool) {
	if ProviderForServiceType(serviceType) == ProviderBunny {
		return "", false
	}
	return ServiceFQDN(serviceType, rootDomain)
}

// BunnyRootServiceFQDN resolves the global root entrypoint for a
// Bunny-managed service (e.g. "foghorn.frameworks.network"). These are
// user-facing geo-routed records served from a per-service Bunny zone
// separate from per-cluster scope and per-tenant aliases. Returns
// false for services that are not Bunny-managed.
//
// Caller MUST ensure the service zone has been delegated to Bunny first
// (DNSManager.EnsureBunnyZone). Issuing records into a non-existent
// Bunny zone fails.
func BunnyRootServiceFQDN(serviceType, rootDomain string) (string, bool) {
	if ProviderForServiceType(serviceType) != ProviderBunny {
		return "", false
	}
	return ServiceFQDN(serviceType, rootDomain)
}

// TenantAliasableServiceTypes returns the public service types that get a
// per-tenant DNS alias under {tenant}.cdn.frameworks.network. This is the
// set of services a paid-tier tenant sees branded URLs for. Excludes
// telemetry (operator-internal) and bridge/decklog (root-scope only).
func TenantAliasableServiceTypes() []string {
	return []string{
		"edge",
		"edge-egress",
		"edge-ingest",
		"edge-storage",
		"edge-processing",
		"chandler",
		"foghorn",
		"livepeer-gateway",
	}
}

// GlobalRootServiceZoneLabels returns the Bunny-delegated root labels used for
// global media entrypoints. This is product surface, not deployment config:
// free/default traffic uses these names, and the tenant alias feature layers on
// top of the same service set.
func GlobalRootServiceZoneLabels() []string {
	out := make([]string, 0, len(TenantAliasableServiceTypes()))
	for _, serviceType := range TenantAliasableServiceTypes() {
		label, ok := PublicSubdomain(serviceType)
		if !ok || label == "" {
			continue
		}
		out = append(out, label)
	}
	slices.Sort(out)
	return out
}

// ServiceFQDN resolves the service label under the supplied DNS scope. For media
// services, pass the media cluster scope such as `ams.example.com`.
func ServiceFQDN(serviceType, rootDomain string) (string, bool) {
	rootDomain = NormalizeDomainScope(rootDomain)
	subdomain, ok := PublicSubdomain(serviceType)
	if !ok {
		return "", false
	}
	if rootDomain == "" {
		return "", false
	}
	if subdomain == "" {
		return rootDomain, true
	}
	return subdomain + "." + rootDomain, true
}

// NormalizeDomainScope converts deployment base_url values into DNS scopes.
// Quartermaster stores this value as operator-facing config, and older
// manifests may include a scheme or path. Public DNS helpers need only the
// hostname portion.
func NormalizeDomainScope(raw string) string {
	value := strings.Trim(strings.TrimSpace(raw), ".")
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "https//"):
		value = "https://" + strings.TrimLeft(value[len("https//"):], "/")
	case strings.HasPrefix(lower, "http//"):
		value = "http://" + strings.TrimLeft(value[len("http//"):], "/")
	}

	parseHost := func(candidate string) string {
		parsed, err := url.Parse(candidate)
		if err != nil {
			return ""
		}
		host := parsed.Hostname()
		if host == "" {
			return ""
		}
		return strings.Trim(strings.ToLower(host), ".")
	}

	if strings.Contains(value, "://") {
		return parseHost(value)
	}
	if strings.HasPrefix(value, "//") {
		return parseHost("https:" + value)
	}
	if host := parseHost("//" + value); host != "" {
		return host
	}
	return strings.ToLower(value)
}

// ClusterSlug returns a DNS-safe slug for a cluster, preferring cluster_id and
// falling back to cluster_name when the ID sanitizes to the default value.
func ClusterSlug(clusterID, clusterName string) string {
	if v := SanitizeLabel(clusterID); v != "default" {
		return v
	}
	return SanitizeLabel(clusterName)
}

// reservedPlatformLabels are labels that can never be used as tenant
// subdomain slugs regardless of dynamic state. Kept here as a static
// list to avoid an env round-trip on every signup validation.
var reservedPlatformLabels = []string{
	"www", "api", "mcp", "app", "admin", "status",
	"mail", "docs", "help", "support", "blog",
	"cdn", "static", "assets", "auth", "login",
}

// reservedPrefixes are subdomain prefixes that collide with reserved
// DNS shapes elsewhere in the system. Tenant slugs starting with any
// of these are rejected.
var reservedPrefixes = []string{
	"edge-", // collides with {node_label}.{cluster}.{root}
}

// ReservedTenantSlugs returns the union of labels that must not be
// used as tenant subdomains:
//   - all managedServiceTypes
//   - all values in publicSubdomains
//   - reservedPlatformLabels (www, api, mcp, cdn, ...)
//   - extra labels passed in (typically active cluster_slug values
//     from infrastructure_clusters)
//
// Validation against this list happens BEFORE writing tenants.subdomain
// in Quartermaster. Reject (not silently sanitize) on collision.
func ReservedTenantSlugs(extraClusterSlugs []string) []string {
	seen := make(map[string]struct{})
	add := func(s string) {
		s = SanitizeLabel(s)
		if s != "" && s != "default" {
			seen[s] = struct{}{}
		}
	}
	for _, t := range managedServiceTypes {
		add(t)
	}
	for _, s := range publicSubdomains {
		add(s)
	}
	for _, l := range reservedPlatformLabels {
		add(l)
	}
	for _, c := range extraClusterSlugs {
		add(c)
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	return out
}

// ReservedTenantSlugPrefixes returns prefixes that disqualify a tenant
// slug (e.g. "edge-" collides with edge node labels).
func ReservedTenantSlugPrefixes() []string {
	out := make([]string, len(reservedPrefixes))
	copy(out, reservedPrefixes)
	return out
}

// IsReservedTenantSlug reports whether a candidate slug collides with
// the reserved set. Caller is expected to have already sanitized the
// label (lowercase, DNS-safe).
func IsReservedTenantSlug(slug string, extraClusterSlugs []string) bool {
	slug = SanitizeLabel(slug)
	if slug == "" || slug == "default" {
		return true
	}
	for _, p := range reservedPrefixes {
		if len(slug) >= len(p) && slug[:len(p)] == p {
			return true
		}
	}
	return slices.Contains(ReservedTenantSlugs(extraClusterSlugs), slug)
}
