package cmd

import (
	"strings"

	"frameworks/cli/pkg/inventory"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// aggregatorRegion returns the region of the aggregator Kafka cluster, or ""
// when the manifest declares none or its region cannot be resolved.
func aggregatorRegion(manifest *inventory.Manifest) string {
	aggregator := aggregatorKafkaClusterView(manifest)
	if aggregator == nil {
		return ""
	}
	if region := strings.TrimSpace(aggregator.RegionID); region != "" {
		return region
	}
	return singleKafkaBrokerRegion(manifest, aggregator)
}

// aggregatorRegionAlias returns the mesh DNS record name for serviceID's
// replicas in the aggregator region, such as "decklog.eu-west", or "" when that
// region is unknown.
func aggregatorRegionAlias(manifest *inventory.Manifest, serviceID string) string {
	region := aggregatorRegion(manifest)
	if region == "" {
		return ""
	}
	return serviceID + "." + pkgdns.SanitizeLabel(region)
}

// privateerAggregatorRegionDNSAliasesForHost returns the services that services
// on selfHostName reach through the target's aggregator-region replicas.
func privateerAggregatorRegionDNSAliasesForHost(manifest *inventory.Manifest, selfHostName string) map[string]struct{} {
	aliases := map[string]struct{}{}
	if manifest == nil || selfHostName == "" {
		return aliases
	}
	for _, serviceID := range privateerLocalServiceIDs(manifest, selfHostName) {
		for _, dep := range topology.AggregatorRegionDNSServiceDependencies(serviceID) {
			if manifestServiceEnabledForDeploy(manifest, dep) {
				aliases[dep] = struct{}{}
			}
		}
	}
	return aliases
}

// serviceProviderHostsInAggregatorRegion returns the hosts running alias in the
// aggregator region. When that region is unknown it returns every provider
// host, which is how a single-region manifest resolves the alias.
func serviceProviderHostsInAggregatorRegion(manifest *inventory.Manifest, alias string) []string {
	hosts := serviceProviderHostsForAliasGlobal(manifest, alias)
	region := aggregatorRegion(manifest)
	if region == "" {
		return hosts
	}
	var inRegion []string
	for _, hostName := range hosts {
		if privateerHostRegion(manifest, hostName) == region {
			inRegion = append(inRegion, hostName)
		}
	}
	return inRegion
}
