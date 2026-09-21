package inventory

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// validateKafkaTopics requires every manifest topic to set retention.ms, so no
// topic falls back to the broker's default retention. A canonical topic
// (pkg/topology) must match its canonical config, partition count, and
// replication factor, because retention is platform policy and the domain
// topic's partition count is part of its keying contract. Every topic
// topology.ClusterTopics requires for the cluster's role must be declared,
// because provisioning only creates declared topics and producers and
// MirrorMaker2 depend on them.
func validateKafkaTopics(label string, topics []KafkaTopic, brokerCount int, aggregator bool) error {
	seen := map[string]bool{}
	for _, topic := range topics {
		if seen[topic.Name] {
			return fmt.Errorf("%s topic %q is declared twice", label, topic.Name)
		}
		seen[topic.Name] = true
		retention, ok := topic.Config["retention.ms"]
		if !ok {
			return fmt.Errorf("%s topic %q: config.retention.ms is required", label, topic.Name)
		}
		if ms, err := strconv.ParseInt(retention, 10, 64); err != nil || ms <= 0 {
			return fmt.Errorf("%s topic %q: config.retention.ms must be a positive number of milliseconds, got %q", label, topic.Name, retention)
		}
		spec, canonical := topology.CanonicalTopic(topic.Name)
		if !canonical {
			continue
		}
		for key, want := range spec.Config {
			if got := topic.Config[key]; got != want {
				return fmt.Errorf("%s topic %q: config.%s must be %q (pkg/topology), got %q", label, topic.Name, key, want, got)
			}
		}
		if spec.Partitions > 0 && topic.Partitions != spec.Partitions {
			return fmt.Errorf("%s topic %q: partitions must be %d, got %d", label, topic.Name, spec.Partitions, topic.Partitions)
		}
		if spec.ReplicationFactor > 0 {
			want := min(spec.ReplicationFactor, brokerCount)
			if topic.ReplicationFactor != want {
				return fmt.Errorf("%s topic %q: replication_factor must be %d with %d brokers, got %d", label, topic.Name, want, brokerCount, topic.ReplicationFactor)
			}
		}
	}
	role := "regional"
	if aggregator {
		role = "aggregator"
	}
	for _, name := range topology.ClusterTopics(aggregator) {
		if seen[name] {
			continue
		}
		return fmt.Errorf("%s: required topic %q is missing (every %s Kafka cluster must declare it); add to its topics list:\n%s",
			label, name, role, canonicalTopicYAML(name, brokerCount))
	}
	return nil
}

// canonicalTopicYAML renders the manifest entry for a canonical topic on a
// cluster with brokerCount brokers. Partition counts the topology leaves to
// the manifest default to 3.
func canonicalTopicYAML(name string, brokerCount int) string {
	spec, _ := topology.CanonicalTopic(name)
	partitions := 3
	if spec.Partitions > 0 {
		partitions = spec.Partitions
	}
	replication := min(3, max(brokerCount, 1))
	if spec.ReplicationFactor > 0 {
		replication = min(spec.ReplicationFactor, max(brokerCount, 1))
	}
	keys := make([]string, 0, len(spec.Config))
	for key := range spec.Config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "  - name: %s\n    partitions: %d\n    replication_factor: %d\n    config:\n", name, partitions, replication)
	for _, key := range keys {
		fmt.Fprintf(&b, "      %s: %q\n", key, spec.Config[key])
	}
	return strings.TrimRight(b.String(), "\n")
}
