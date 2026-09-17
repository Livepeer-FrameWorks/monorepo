package orchestrator

import (
	"fmt"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
)

// kafkaMirrorMakerWorkerHosts validates MirrorMaker2 links and returns the
// sorted, de-duplicated worker hosts. Every ordered pair of declared Kafka
// regions must have exactly one link, and a link's workers must run in its
// target region so replicated writes stay local to the cluster they land in.
func (p *Planner) kafkaMirrorMakerWorkerHosts(links []inventory.KafkaMirrorLink) ([]string, error) {
	if len(links) == 0 {
		return nil, fmt.Errorf("kafka mirrormaker is enabled but declares no links")
	}
	regions, err := p.kafkaRegionIDs()
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(regions))
	for _, region := range regions {
		known[region] = true
	}

	declared := make(map[[2]string]bool, len(links))
	hostSet := make(map[string]bool)
	for _, link := range links {
		source := strings.TrimSpace(link.Source)
		target := strings.TrimSpace(link.Target)
		name := source + "->" + target
		switch {
		case source == "" || target == "":
			return nil, fmt.Errorf("kafka mirrormaker link %q needs both source and target regions", name)
		case source == target:
			return nil, fmt.Errorf("kafka mirrormaker link %s mirrors a region into itself", name)
		case !known[source]:
			return nil, fmt.Errorf("kafka mirrormaker link %s: source %q is not a declared Kafka region (declared: %s)", name, source, strings.Join(regions, ", "))
		case !known[target]:
			return nil, fmt.Errorf("kafka mirrormaker link %s: target %q is not a declared Kafka region (declared: %s)", name, target, strings.Join(regions, ", "))
		case declared[[2]string{source, target}]:
			return nil, fmt.Errorf("kafka mirrormaker link %s is declared more than once", name)
		}
		declared[[2]string{source, target}] = true

		if len(link.Hosts) == 0 {
			return nil, fmt.Errorf("kafka mirrormaker link %s declares no worker hosts", name)
		}
		for _, host := range link.Hosts {
			host = strings.TrimSpace(host)
			if host == "" {
				return nil, fmt.Errorf("kafka mirrormaker link %s has an empty worker host", name)
			}
			if region := p.hostRegion(host); region != target {
				return nil, fmt.Errorf("kafka mirrormaker link %s worker host %q is in region %q, want target region %q", name, host, region, target)
			}
			hostSet[host] = true
		}
	}

	for _, source := range regions {
		for _, target := range regions {
			if source != target && !declared[[2]string{source, target}] {
				return nil, fmt.Errorf("kafka mirrormaker has no link %s->%s; every pair of Kafka regions needs a link in each direction", source, target)
			}
		}
	}

	hosts := make([]string, 0, len(hostSet))
	for host := range hostSet {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts, nil
}

// kafkaRegionIDs returns the sorted region of every declared Kafka cluster,
// taken from its region_id or inferred from its node host labels.
func (p *Planner) kafkaRegionIDs() ([]string, error) {
	clusters := p.kafkaClusters()
	regions := make([]string, 0, len(clusters))
	seen := make(map[string]bool, len(clusters))
	for _, kc := range clusters {
		region := strings.TrimSpace(kc.RegionID)
		if region == "" {
			region = p.kafkaNodeRegion(kc.Controllers, kc.Brokers)
		}
		if region == "" {
			return nil, fmt.Errorf("kafka cluster %s has no region_id and its hosts carry no region label", kafkaClusterHostList(kc))
		}
		if seen[region] {
			return nil, fmt.Errorf("kafka region %q is declared by more than one cluster", region)
		}
		seen[region] = true
		regions = append(regions, region)
	}
	sort.Strings(regions)
	return regions, nil
}

func kafkaClusterHostList(kc kafkaPlannerCluster) string {
	hosts := make([]string, 0, len(kc.Controllers)+len(kc.Brokers))
	for _, controller := range kc.Controllers {
		hosts = append(hosts, controller.Host)
	}
	for _, broker := range kc.Brokers {
		hosts = append(hosts, broker.Host)
	}
	return "[" + strings.Join(hosts, ", ") + "]"
}
