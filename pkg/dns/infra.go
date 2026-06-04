package dns

import (
	"fmt"
	"slices"
)

// InfraZoneLabel is the dedicated namespace for physical service-instance
// endpoints, kept separate from the per-service global media zones
// (GlobalRootServiceZoneLabels). Physical identity lives under
// <service>.<node>.infra.<root> so it never leaks media-cluster membership.
const InfraZoneLabel = "infra"

// InfraZoneFQDN returns the delegated physical-endpoint zone, e.g.
// "infra.frameworks.network". Navigator must ensure this zone is delegated to
// Bunny before issuing records or DNS-01 challenges under it.
func InfraZoneFQDN(rootDomain string) string {
	root := NormalizeDomainScope(rootDomain)
	if root == "" {
		return ""
	}
	return InfraZoneLabel + "." + root
}

// PhysicalEndpointServiceTypes returns the service types that get per-instance
// physical endpoints published under the infra namespace. Today only the
// Livepeer gateway needs explicit instance addressing (for the MistProcLivepeer
// broadcaster failover set); other pool services keep using pooled DNS.
func PhysicalEndpointServiceTypes() []string {
	return []string{"livepeer-gateway"}
}

// IsPhysicalEndpointServiceType reports whether a service type publishes
// per-instance physical endpoints under the infra namespace.
func IsPhysicalEndpointServiceType(serviceType string) bool {
	return slices.Contains(PhysicalEndpointServiceTypes(), serviceType)
}

// InfraInstanceFQDN returns the physical endpoint for one running service
// instance: {service}.{node}.infra.{root}, e.g.
// "livepeer-gateway.regional-eu-2.infra.frameworks.network". The service label
// is the concrete service type (not its pooled PublicSubdomain), so the gateway
// instance is named "livepeer-gateway", not "livepeer". Returns false when the
// node identity or root is missing.
func InfraInstanceFQDN(serviceType, nodeID, rootDomain string) (string, bool) {
	root := NormalizeDomainScope(rootDomain)
	if root == "" {
		return "", false
	}
	svc := SanitizeLabel(serviceType)
	node := SanitizeLabel(nodeID)
	if svc == "default" || node == "default" {
		return "", false
	}
	return fmt.Sprintf("%s.%s.%s.%s", svc, node, InfraZoneLabel, root), true
}
