// Package clusterderive holds pure-function helpers that translate cluster-manifest
// state into ingress, service-registry, and TLS-bundle desired state. The CLI
// bootstrap chain (cli/cmd) and the bootstrap-desired-state renderer
// (cli/pkg/bootstrap) both consume these helpers so the public-service surface,
// FQDN derivation, and cluster-scoped subdomain rules stay in lockstep.
package clusterderive

import (
	"slices"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
)

// PublicServiceType maps a manifest service name to its DNS subdomain type. Returns
// false for non-public services (databases, internal control plane). This list is
// the single source of truth for "is this a public-facing service?".
func PublicServiceType(serviceName string) (string, bool) {
	switch serviceName {
	case "bridge":
		return "bridge", true
	case "chandler":
		return "chandler", true
	case "foghorn":
		return "foghorn", true
	case "chartroom":
		return "chartroom", true
	case "foredeck":
		return "foredeck", true
	case "logbook":
		return "logbook", true
	case "steward":
		return "steward", true
	case "listmonk":
		return "listmonk", true
	case "chatwoot":
		return "chatwoot", true
	case "grafana":
		return "grafana", true
	case "metabase":
		return "metabase", true
	case "livepeer-gateway":
		return "livepeer-gateway", true
	case "vmauth":
		return "telemetry", true
	}
	return "", false
}

// ManifestServiceType maps a manifest service key to the public service type it
// implements. Aliased services such as foghorn-eu use svc.Deploy to point back
// at the canonical public service surface.
func ManifestServiceType(serviceName string, svc inventory.ServiceConfig) (string, bool) {
	if serviceType, ok := PublicServiceType(serviceName); ok {
		return serviceType, true
	}
	if deploy := strings.TrimSpace(svc.Deploy); deploy != "" {
		return PublicServiceType(deploy)
	}
	return "", false
}

// SelfRegisters reports services that create their own service_registry rows at
// startup via Quartermaster.BootstrapService. Bootstrap must not pre-register these,
// or the runtime BootstrapService call collides with the bootstrap-seeded row.
func SelfRegisters(serviceName string) bool {
	switch serviceName {
	case "bridge", "foghorn", "chandler":
		return true
	}
	return false
}

// TLSBundleID derives a deterministic, filesystem-safe TLS bundle id from a kind +
// root domain. Privateer uses the id as a path component beneath ingress.TLSRoot, so
// the result must be filesystem-safe (dots → hyphens, wildcard markers expanded,
// lowercased).
func TLSBundleID(kind, rootDomain string) string {
	rootDomain = pkgdns.NormalizeDomainScope(rootDomain)
	replacer := strings.NewReplacer(".", "-", "*", "wildcard-", " ", "-")
	return strings.ToLower(kind + "-" + replacer.Replace(rootDomain))
}

// ClusterScopedRootDomain returns "<cluster-slug>.<root-domain>" for cluster-scoped
// services. Empty when no root domain is configured or the cluster slug can't be
// derived (cluster missing from manifest, slug rules reject the inputs, etc.).
func ClusterScopedRootDomain(manifest *inventory.Manifest, clusterID string) string {
	rootDomain := ""
	if manifest != nil {
		rootDomain = pkgdns.NormalizeDomainScope(manifest.RootDomain)
	}
	if manifest == nil || rootDomain == "" || clusterID == "" {
		return ""
	}
	clusterName := ""
	if cfg, ok := manifest.Clusters[clusterID]; ok {
		clusterName = cfg.Name
	}
	clusterSlug := pkgdns.ClusterSlug(clusterID, clusterName)
	if clusterSlug == "" {
		return ""
	}
	return clusterSlug + "." + rootDomain
}

// PublicServiceRootDomain returns the root domain a public service's FQDN sits
// beneath. Cluster-scoped services use "<cluster-slug>.<root-domain>"; the rest use
// the bare root domain.
func PublicServiceRootDomain(serviceType string, manifest *inventory.Manifest, clusterID string) string {
	if manifest == nil {
		return ""
	}
	if pkgdns.IsClusterScopedServiceType(serviceType) {
		return ClusterScopedRootDomain(manifest, clusterID)
	}
	return pkgdns.NormalizeDomainScope(manifest.RootDomain)
}

// PlatformGlobalRootIngressDomainsForService returns the root media FQDNs
// served by platform-operated pool services, such as foghorn.frameworks.network.
func PlatformGlobalRootIngressDomainsForService(serviceName string, svc inventory.ServiceConfig, manifest *inventory.Manifest, clusterID string) ([]string, string) {
	serviceType, ok := ManifestServiceType(serviceName, svc)
	if !ok || !isPlatformGlobalRootServiceType(serviceType) {
		return nil, ""
	}
	if !IsPlatformOfficialCluster(manifest, clusterID) {
		return nil, ""
	}
	rootDomain := pkgdns.NormalizeDomainScope(manifest.RootDomain)
	if rootDomain == "" {
		return nil, ""
	}
	fqdn, ok := pkgdns.BunnyRootServiceFQDN(serviceType, rootDomain)
	if !ok || fqdn == "" {
		return nil, ""
	}
	return []string{fqdn}, TLSBundleID("wildcard", rootDomain)
}

func isPlatformGlobalRootServiceType(serviceType string) bool {
	switch serviceType {
	case "foghorn", "chandler", "livepeer-gateway":
		return true
	default:
		return false
	}
}

func IsPlatformOfficialCluster(manifest *inventory.Manifest, clusterID string) bool {
	if manifest == nil || clusterID == "" {
		return false
	}
	cluster, ok := manifest.Clusters[clusterID]
	if !ok {
		return false
	}
	return cluster.PlatformOfficial || strings.TrimSpace(cluster.Class) == "platform_official"
}

// LogicalServiceClusterIDs returns the full set of logical media clusters a
// cluster-scoped Bunny service is assigned to. Resolution order:
//  1. svc.Clusters (M:N explicit list)
//  2. svc.Cluster (singular shorthand)
//  3. manifest's default media cluster (Bunny services only)
//
// Returns nil for services that are not cluster-scoped Bunny services or for
// which no media cluster can be resolved.
func LogicalServiceClusterIDs(serviceName string, svc inventory.ServiceConfig, manifest *inventory.Manifest) []string {
	if len(svc.Clusters) > 0 {
		out := make([]string, 0, len(svc.Clusters))
		for _, c := range svc.Clusters {
			c = strings.TrimSpace(c)
			if c != "" {
				out = append(out, c)
			}
		}
		return out
	}
	if c := strings.TrimSpace(svc.Cluster); c != "" {
		return []string{c}
	}
	serviceType, ok := ManifestServiceType(serviceName, svc)
	if !ok || pkgdns.ProviderForServiceType(serviceType) != pkgdns.ProviderBunny {
		return nil
	}
	if serviceType == "telemetry" {
		return MediaClusterIDs(manifest)
	}
	if clusterID := defaultMediaClusterID(manifest); clusterID != "" {
		return []string{clusterID}
	}
	return nil
}

func HostScopedLogicalServiceClusterIDs(serviceName string, svc inventory.ServiceConfig, manifest *inventory.Manifest, hostKey string) ([]string, bool) {
	clusterIDs := LogicalServiceClusterIDs(serviceName, svc, manifest)
	if len(clusterIDs) == 0 {
		return nil, false
	}
	serviceType, ok := ManifestServiceType(serviceName, svc)
	if !ok || serviceType != "telemetry" || manifest == nil {
		return clusterIDs, true
	}
	hostRegion := HostRegion(manifest, hostKey)
	if hostRegion == "" {
		return clusterIDs, true
	}
	filtered := make([]string, 0, len(clusterIDs))
	hasRegionalTarget := false
	for _, clusterID := range clusterIDs {
		cluster := manifest.Clusters[clusterID]
		targetRegion := strings.TrimSpace(cluster.Region)
		if targetRegion == "" {
			continue
		}
		hasRegionalTarget = true
		if targetRegion == hostRegion {
			filtered = append(filtered, clusterID)
		}
	}
	if !hasRegionalTarget {
		return clusterIDs, true
	}
	return filtered, true
}

func HostRegion(manifest *inventory.Manifest, hostKey string) string {
	if manifest == nil || hostKey == "" {
		return ""
	}
	host, ok := manifest.GetHost(hostKey)
	if !ok {
		return ""
	}
	if region := strings.TrimSpace(host.Labels["region"]); region != "" {
		return region
	}
	if cluster, ok := manifest.Clusters[host.Cluster]; ok {
		return strings.TrimSpace(cluster.Region)
	}
	return ""
}

func defaultMediaClusterID(manifest *inventory.Manifest) string {
	mediaClusters := MediaClusterIDs(manifest)
	if len(mediaClusters) == 0 {
		return ""
	}
	for _, clusterID := range mediaClusters {
		if manifest.Clusters[clusterID].Default {
			return clusterID
		}
	}
	if len(mediaClusters) == 1 {
		return mediaClusters[0]
	}
	return ""
}

// MediaClusterIDs returns cluster IDs that should own Bunny media DNS zones.
func MediaClusterIDs(manifest *inventory.Manifest) []string {
	if manifest == nil {
		return nil
	}
	var out []string
	for clusterID, cluster := range manifest.Clusters {
		isMedia := cluster.Type == "edge" || slices.Contains(cluster.Roles, "media")
		if !isMedia {
			continue
		}
		out = append(out, clusterID)
	}
	sort.Strings(out)
	return out
}

// WildcardBundleDomains returns the SANs for a wildcard bundle keyed off rootDomain:
// the bare root and the wildcard. Used by bootstrap to populate TLSBundle.Domains so
// the issued cert covers both the apex (e.g. frameworks.network) and any subdomain
// served via this bundle (e.g. chatwoot.frameworks.network).
func WildcardBundleDomains(rootDomain string) []string {
	rootDomain = pkgdns.NormalizeDomainScope(rootDomain)
	if rootDomain == "" {
		return nil
	}
	return []string{rootDomain, "*." + rootDomain}
}

// AutoIngressDomains returns the FQDN list and TLS bundle id for a public service in
// the given cluster. Foredeck is the apex case (root domain + www); other public
// services use a wildcard bundle keyed off their cluster-scoped root.
func AutoIngressDomains(serviceName string, manifest *inventory.Manifest, clusterID string) ([]string, string) {
	return AutoIngressDomainsForService(serviceName, inventory.ServiceConfig{}, manifest, clusterID)
}

// AutoIngressDomainsForService is AutoIngressDomains with manifest alias support.
func AutoIngressDomainsForService(serviceName string, svc inventory.ServiceConfig, manifest *inventory.Manifest, clusterID string) ([]string, string) {
	serviceType, ok := ManifestServiceType(serviceName, svc)
	if !ok {
		return nil, ""
	}
	if serviceType == "foredeck" {
		if manifest == nil || manifest.RootDomain == "" {
			return nil, ""
		}
		return []string{manifest.RootDomain, "www." + manifest.RootDomain}, TLSBundleID("apex", manifest.RootDomain)
	}

	rootDomain := PublicServiceRootDomain(serviceType, manifest, clusterID)
	if rootDomain == "" {
		return nil, ""
	}
	fqdn, ok := pkgdns.ServiceFQDN(serviceType, rootDomain)
	if !ok || fqdn == "" {
		return nil, ""
	}
	return []string{fqdn}, TLSBundleID("wildcard", rootDomain)
}
