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
	pkgdns "frameworks/pkg/dns"
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
	replacer := strings.NewReplacer(".", "-", "*", "wildcard-", " ", "-")
	return strings.ToLower(kind + "-" + replacer.Replace(rootDomain))
}

// ClusterScopedRootDomain returns "<cluster-slug>.<root-domain>" for cluster-scoped
// services. Empty when no root domain is configured or the cluster slug can't be
// derived (cluster missing from manifest, slug rules reject the inputs, etc.).
func ClusterScopedRootDomain(manifest *inventory.Manifest, clusterID string) string {
	if manifest == nil || manifest.RootDomain == "" || clusterID == "" {
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
	return clusterSlug + "." + manifest.RootDomain
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
	return strings.TrimSpace(manifest.RootDomain)
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
	serviceType, ok := PublicServiceType(serviceName)
	if !ok || pkgdns.ProviderForServiceType(serviceType) != pkgdns.ProviderBunny {
		return nil
	}
	if clusterID := defaultMediaClusterID(manifest); clusterID != "" {
		return []string{clusterID}
	}
	return nil
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
	rootDomain = strings.TrimSpace(rootDomain)
	if rootDomain == "" {
		return nil
	}
	return []string{rootDomain, "*." + rootDomain}
}

// AutoIngressDomains returns the FQDN list and TLS bundle id for a public service in
// the given cluster. Foredeck is the apex case (root domain + www); other public
// services use a wildcard bundle keyed off their cluster-scoped root.
func AutoIngressDomains(serviceName string, manifest *inventory.Manifest, clusterID string) ([]string, string) {
	if serviceName == "foredeck" {
		if manifest == nil || manifest.RootDomain == "" {
			return nil, ""
		}
		return []string{manifest.RootDomain, "www." + manifest.RootDomain}, TLSBundleID("apex", manifest.RootDomain)
	}

	serviceType, ok := PublicServiceType(serviceName)
	if !ok {
		return nil, ""
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
