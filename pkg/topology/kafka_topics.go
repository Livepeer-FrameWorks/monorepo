package topology

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Canonical Kafka topic names shared by Decklog, the regional and aggregate
// consumers, and MirrorMaker2 provisioning. Producers and consumers read these
// as defaults so a topic cannot be mirrored without a matching consumer.
const (
	TopicAnalyticsEvents     = "analytics_events"
	TopicServiceEvents       = "service_events"
	TopicRawMistTriggers     = "analytics.raw_mist_triggers"
	TopicBillingUsageReports = "billing.usage_reports"
	TopicDecklogDLQ          = "decklog_events_dlq"
	// TopicLookoutIncidents exists only on the aggregator Kafka cluster: Lookout
	// and Skipper both run in the aggregator region, so it is never mirrored.
	TopicLookoutIncidents = "lookout.incidents"
	// TopicDomainEvents carries the registered domain events (pkg/events) from
	// every producer outbox, keyed "<aggregate>/<id>" so each aggregate's
	// events stay on one partition. Decklog is its only producer.
	TopicDomainEvents = "domain.events"
)

const day = 24 * time.Hour

// TopicSpec is the canonical definition of a Kafka topic. Config holds the
// topic-level settings every deployment must apply, always including
// retention.ms. Partitions is non-zero only where the count is part of the
// contract: records keyed by aggregate move to a different partition when the
// count changes, so the domain topic's count is fixed.
type TopicSpec struct {
	Name string
	// Partitions is the required partition count, or 0 when the manifest
	// chooses it.
	Partitions int
	// ReplicationFactor is the replication factor on a cluster with at least
	// that many brokers, or 0 when the manifest chooses it.
	ReplicationFactor int
	Retention         time.Duration
	Config            map[string]string
}

// CanonicalTopics returns every canonical topic with its retention. Each call
// returns fresh values, so callers may modify them.
func CanonicalTopics() []TopicSpec {
	specs := []TopicSpec{
		{Name: TopicAnalyticsEvents, Retention: 7 * day},
		{Name: TopicServiceEvents, Retention: 180 * day},
		{Name: TopicRawMistTriggers, Retention: 30 * day},
		{Name: TopicBillingUsageReports, Retention: 365 * day},
		{Name: TopicDecklogDLQ, Retention: 90 * day},
		{Name: TopicLookoutIncidents, Retention: 7 * day},
		{
			Name: TopicDomainEvents, Partitions: 12, ReplicationFactor: 3, Retention: 180 * day,
			Config: map[string]string{"cleanup.policy": "delete"},
		},
	}
	for i := range specs {
		cfg := map[string]string{"retention.ms": strconv.FormatInt(specs[i].Retention.Milliseconds(), 10)}
		for k, v := range specs[i].Config {
			cfg[k] = v
		}
		specs[i].Config = cfg
	}
	return specs
}

// CanonicalTopic returns the canonical definition of name.
func CanonicalTopic(name string) (TopicSpec, bool) {
	for _, spec := range CanonicalTopics() {
		if spec.Name == name {
			return spec, true
		}
	}
	return TopicSpec{}, false
}

// RegionalToAggregatorTopics lists the topics every regional Kafka cluster
// mirrors into the aggregator. The aggregator feeds central ClickHouse and
// billing, so the set includes the raw final-trigger journal that final facts
// and metering are projected from, and the domain events the aggregator's
// projections and webhook delivery consume.
func RegionalToAggregatorTopics() []string {
	return []string{
		TopicAnalyticsEvents,
		TopicServiceEvents,
		TopicRawMistTriggers,
		TopicBillingUsageReports,
		TopicDecklogDLQ,
		TopicDomainEvents,
	}
}

// AggregatorToRegionalTopics lists the realtime topics mirrored out of the
// aggregator so each regional Signalman observes events produced in other
// regions. Domain events are realtime too: Signalman delivers the public ones
// to tenant subscribers. Durable-only topics stay on the regional-to-aggregator
// path.
func AggregatorToRegionalTopics() []string {
	return []string{
		TopicAnalyticsEvents,
		TopicServiceEvents,
		TopicDomainEvents,
	}
}

// ClusterTopics lists the canonical topics a Kafka cluster must declare. A
// regional cluster holds the topics its Decklog produces and MirrorMaker2
// reads in either mirror direction. The aggregator holds every canonical
// topic, including the unmirrored Lookout incidents. The result is sorted.
func ClusterTopics(aggregator bool) []string {
	set := map[string]struct{}{}
	if aggregator {
		for _, spec := range CanonicalTopics() {
			set[spec.Name] = struct{}{}
		}
	}
	for _, name := range RegionalToAggregatorTopics() {
		set[name] = struct{}{}
	}
	for _, name := range AggregatorToRegionalTopics() {
		set[name] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MirroredTopicName is the name MirrorMaker2's default replication policy
// gives topic when it is replicated from the cluster aliased sourceAlias.
func MirroredTopicName(sourceAlias, topic string) string {
	return sourceAlias + "." + topic
}

// MirrorExcludePattern returns the MirrorMaker2 topics.exclude regex matching
// topics that are already mirrored copies from any of the given cluster
// aliases. Every link sets it so a copy is never re-replicated into a
// transitive "<a>.<b>.<topic>" name. It returns "" when no aliases are known.
func MirrorExcludePattern(aliases []string) string {
	seen := make(map[string]struct{}, len(aliases))
	unique := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		seen[alias] = struct{}{}
		unique = append(unique, regexp.QuoteMeta(alias))
	}
	if len(unique) == 0 {
		return ""
	}
	sort.Strings(unique)
	return `^(` + strings.Join(unique, "|") + `)\..*`
}
