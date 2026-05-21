package dns

import (
	"fmt"
	"strings"
)

// EdgeNodeLabel returns the DNS label for an edge node record under its
// cluster zone. Sanitizes the node ID and prefixes "edge-" unless the
// sanitized label already starts with it. The result is reserved against
// tenant slugs via ReservedTenantSlugPrefixes.
func EdgeNodeLabel(nodeID string) string {
	label := SanitizeLabel(nodeID)
	if strings.HasPrefix(label, "edge-") {
		return label
	}
	return "edge-" + label
}

// EdgeNodeFQDN returns the fully-qualified edge node domain
// {edge-<node>}.{cluster_slug}.{root_domain}. clusterSlug must already be
// sanitized (callers typically use SanitizeLabel or ClusterSlug).
func EdgeNodeFQDN(nodeID, clusterSlug, rootDomain string) string {
	return fmt.Sprintf("%s.%s.%s", EdgeNodeLabel(nodeID), clusterSlug, rootDomain)
}
