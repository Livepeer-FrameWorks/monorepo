package cmd

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// addKafkaMirrorMakerLinkMetadata fills MirrorMaker2 worker metadata for one
// host. The target is the host's own Kafka region and the sources are the
// links into that region that list the host as a worker.
func addKafkaMirrorMakerLinkMetadata(metadata map[string]any, manifest *inventory.Manifest, task *orchestrator.Task) error {
	mm := manifest.Infrastructure.Kafka.MirrorMaker
	region := manifestTaskRegion(manifest, task)
	target := kafkaClusterViewByAlias(manifest, region)
	if target == nil {
		return fmt.Errorf("kafka-mirrormaker task %s: host %q region %q has no Kafka cluster", task.Name, task.Host, region)
	}
	targetAlias := kafkaClusterAlias(manifest, target)
	aggregatorAlias := kafkaClusterAlias(manifest, aggregatorKafkaClusterView(manifest))

	sources := make([]map[string]any, 0, len(mm.Links))
	for _, link := range mm.Links {
		if strings.TrimSpace(link.Target) != targetAlias || !slices.Contains(link.Hosts, task.Host) {
			continue
		}
		source := kafkaClusterViewByAlias(manifest, strings.TrimSpace(link.Source))
		if source == nil {
			return fmt.Errorf("kafka-mirrormaker link %s->%s: no Kafka cluster for source region", link.Source, link.Target)
		}
		entry := map[string]any{
			"alias":             kafkaClusterAlias(manifest, source),
			"region_id":         source.RegionID,
			"bootstrap_servers": kafkaBrokersBootstrap(manifest, source),
			"topics":            strings.Join(mirrorLinkTopics(link, aggregatorAlias), ","),
			"replicas":          replicasForCluster(source, 0),
			// Consumer-group checkpoints only matter where durable consumer
			// groups could fail over, which is the aggregator.
			"emit_checkpoints": targetAlias == aggregatorAlias,
		}
		tasksMax := link.TaskCount
		if tasksMax <= 0 {
			tasksMax = mm.TaskCount
		}
		if tasksMax > 0 {
			entry["tasks_max"] = tasksMax
		}
		sources = append(sources, entry)
	}
	if len(sources) == 0 {
		return fmt.Errorf("kafka-mirrormaker task %s: host %q is not a worker for any link into %q", task.Name, task.Host, targetAlias)
	}

	metadata["target"] = map[string]any{
		"alias":             targetAlias,
		"region_id":         target.RegionID,
		"bootstrap_servers": kafkaBrokersBootstrap(manifest, target),
		"replicas":          replicasForCluster(target, mm.Replicas),
	}
	metadata["sources"] = sources
	metadata["local_cluster_alias"] = targetAlias
	metadata["exclude_pattern"] = topology.MirrorExcludePattern(kafkaClusterAliases(manifest))
	return nil
}

// mirrorLinkTopics returns the topics a MirrorMaker2 link replicates. Links into
// the aggregator carry the durable analytics and billing set; every other link
// carries only the realtime set regional Signalman consumes.
func mirrorLinkTopics(link inventory.KafkaMirrorLink, aggregatorAlias string) []string {
	if len(link.Topics) > 0 {
		return link.Topics
	}
	if strings.TrimSpace(link.Target) == aggregatorAlias {
		return topology.RegionalToAggregatorTopics()
	}
	return topology.AggregatorToRegionalTopics()
}

// mirroredSourceAliases returns the source aliases of the enabled MirrorMaker2
// links into clusterAlias. These are the prefixes of the topic copies present in
// that Kafka cluster.
func mirroredSourceAliases(manifest *inventory.Manifest, clusterAlias string) []string {
	if manifest == nil || manifest.Infrastructure.Kafka == nil || clusterAlias == "" {
		return nil
	}
	mm := manifest.Infrastructure.Kafka.MirrorMaker
	if mm == nil || !mm.Enabled {
		return nil
	}
	var aliases []string
	for _, link := range mm.Links {
		source := strings.TrimSpace(link.Source)
		if strings.TrimSpace(link.Target) != clusterAlias || source == "" || slices.Contains(aliases, source) {
			continue
		}
		aliases = append(aliases, source)
	}
	sort.Strings(aliases)
	return aliases
}

// kafkaClusterForServiceTask returns the Kafka cluster a service task binds.
// Central consumers and producers (analytics ingest, billing, federation
// control) bind the aggregator regardless of where the binary runs, so a
// regional host placement cannot route them at a local cluster and dual-write
// ClickHouse or miss billing rows. Every other service binds the cluster of its
// media cluster or host region.
func kafkaClusterForServiceTask(manifest *inventory.Manifest, task *orchestrator.Task) *kafkaClusterView {
	if isAggregatorPinnedService(task.Type) {
		if kc := aggregatorKafkaClusterView(manifest); kc != nil {
			return kc
		}
		return &kafkaClusterView{}
	}
	return serviceKafkaCluster(manifest, task.ClusterID, manifestTaskRegion(manifest, task))
}

// validateAggregatorPinnedPlacement rejects an aggregator-pinned service placed
// outside the aggregator Kafka region. Those services read and write the
// aggregator Kafka, central ClickHouse, and billing state, so a replica in
// another region sends its whole consumer or producer traffic across the WAN;
// regional ingest requires a regional ClickHouse target first.
func validateAggregatorPinnedPlacement(manifest *inventory.Manifest, task *orchestrator.Task) error {
	if !isAggregatorPinnedService(task.Type) {
		return nil
	}
	region := aggregatorRegion(manifest)
	hostRegion := manifestTaskRegion(manifest, task)
	if region == "" || hostRegion == "" || hostRegion == region {
		return nil
	}
	return fmt.Errorf("%s on host %q is in region %q but binds the aggregator Kafka in region %q; place it in %q",
		task.Type, task.Host, hostRegion, region, region)
}

func kafkaClusterViewByAlias(manifest *inventory.Manifest, alias string) *kafkaClusterView {
	if alias == "" {
		return nil
	}
	views := allKafkaClusters(manifest)
	for i := range views {
		if kafkaClusterAlias(manifest, &views[i]) == alias {
			return &views[i]
		}
	}
	return nil
}

func kafkaClusterAliases(manifest *inventory.Manifest) []string {
	views := allKafkaClusters(manifest)
	aliases := make([]string, 0, len(views))
	for i := range views {
		aliases = append(aliases, kafkaClusterAlias(manifest, &views[i]))
	}
	return aliases
}
