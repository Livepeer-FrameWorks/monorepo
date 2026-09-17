package topology

import (
	"regexp"
	"sort"
	"strings"
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
)

// RegionalToAggregatorTopics lists the topics every regional Kafka cluster
// mirrors into the aggregator. The aggregator feeds central ClickHouse and
// billing, so the set includes the raw final-trigger journal that final facts
// and metering are projected from.
func RegionalToAggregatorTopics() []string {
	return []string{
		TopicAnalyticsEvents,
		TopicServiceEvents,
		TopicRawMistTriggers,
		TopicBillingUsageReports,
		TopicDecklogDLQ,
	}
}

// AggregatorToRegionalTopics lists the realtime topics mirrored out of the
// aggregator so each regional Signalman observes events produced in other
// regions. Durable-only topics stay on the regional-to-aggregator path.
func AggregatorToRegionalTopics() []string {
	return []string{
		TopicAnalyticsEvents,
		TopicServiceEvents,
	}
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
