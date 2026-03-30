package dns

var managedServiceTypes = []string{
	"edge",
	"edge-egress",
	"edge-ingest",
	"edge-storage",
	"edge-processing",
	"foghorn",
	"livepeer-gateway",
	"bridge",
	"chartroom",
	"foredeck",
	"logbook",
	"steward",
	"listmonk",
	"chatwoot",
}

var clusterScopedServiceTypes = map[string]struct{}{
	"edge":             {},
	"edge-egress":      {},
	"edge-ingest":      {},
	"edge-storage":     {},
	"edge-processing":  {},
	"foghorn":          {},
	"livepeer-gateway": {},
}

var publicSubdomains = map[string]string{
	"edge":             "edge",
	"edge-egress":      "edge-egress",
	"edge-ingest":      "edge-ingest",
	"edge-storage":     "edge-storage",
	"edge-processing":  "edge-processing",
	"foghorn":          "foghorn",
	"livepeer-gateway": "livepeer",
	"bridge":           "bridge",
	"chartroom":        "chartroom",
	"foredeck":         "",
	"logbook":          "logbook",
	"steward":          "steward",
	"listmonk":         "listmonk",
	"chatwoot":         "chatwoot",
}

// ManagedServiceTypes returns the public service types Navigator reconciles.
func ManagedServiceTypes() []string {
	out := make([]string, len(managedServiceTypes))
	copy(out, managedServiceTypes)
	return out
}

// IsClusterScopedServiceType reports whether Navigator should also reconcile
// per-cluster records for the given service type.
func IsClusterScopedServiceType(serviceType string) bool {
	_, ok := clusterScopedServiceTypes[serviceType]
	return ok
}

// PublicSubdomain maps an internal service type to its public DNS label.
// The empty string represents the zone apex.
func PublicSubdomain(serviceType string) (string, bool) {
	subdomain, ok := publicSubdomains[serviceType]
	return subdomain, ok
}

// ServiceFQDN resolves the public FQDN for a managed service type under the
// given root domain.
func ServiceFQDN(serviceType, rootDomain string) (string, bool) {
	subdomain, ok := PublicSubdomain(serviceType)
	if !ok {
		return "", false
	}
	if subdomain == "" {
		return rootDomain, true
	}
	return subdomain + "." + rootDomain, true
}

// ClusterSlug returns a DNS-safe slug for a cluster, preferring cluster_id and
// falling back to cluster_name when the ID sanitizes to the default value.
func ClusterSlug(clusterID, clusterName string) string {
	if v := SanitizeLabel(clusterID); v != "default" {
		return v
	}
	return SanitizeLabel(clusterName)
}
