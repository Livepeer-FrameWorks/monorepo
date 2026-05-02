package dns

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

// PublicSubdomain maps an internal service type to its public DNS label.
// The empty string represents the zone apex.
func PublicSubdomain(serviceType string) (string, bool) {
	subdomain, ok := publicSubdomains[serviceType]
	return subdomain, ok
}

// RootServiceFQDN resolves root/API/web/admin service names. Media services are
// only published under media cluster scopes.
func RootServiceFQDN(serviceType, rootDomain string) (string, bool) {
	if ProviderForServiceType(serviceType) == ProviderBunny {
		return "", false
	}
	return ServiceFQDN(serviceType, rootDomain)
}

// ServiceFQDN resolves the service label under the supplied DNS scope. For media
// services, pass the media cluster scope such as `ams.example.com`.
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
