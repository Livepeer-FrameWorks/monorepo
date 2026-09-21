package topology

import (
	"regexp"
	"slices"
	"testing"
)

func TestRegionalToAggregatorTopicsCarryDurableJournal(t *testing.T) {
	topics := RegionalToAggregatorTopics()
	for _, want := range []string{
		TopicAnalyticsEvents,
		TopicServiceEvents,
		TopicRawMistTriggers,
		TopicBillingUsageReports,
		TopicDecklogDLQ,
		TopicDomainEvents,
	} {
		if !slices.Contains(topics, want) {
			t.Fatalf("RegionalToAggregatorTopics() = %v, missing %q", topics, want)
		}
	}

	topics[0] = "mutated"
	if RegionalToAggregatorTopics()[0] != TopicAnalyticsEvents {
		t.Fatal("RegionalToAggregatorTopics returned shared backing storage")
	}
}

func TestAggregatorToRegionalTopicsAreRealtimeOnly(t *testing.T) {
	got := AggregatorToRegionalTopics()
	want := []string{TopicAnalyticsEvents, TopicServiceEvents, TopicDomainEvents}
	if !slices.Equal(got, want) {
		t.Fatalf("AggregatorToRegionalTopics() = %v, want %v", got, want)
	}
	for _, durable := range []string{TopicRawMistTriggers, TopicBillingUsageReports, TopicDecklogDLQ} {
		if slices.Contains(got, durable) {
			t.Fatalf("realtime mirror set carries durable topic %q", durable)
		}
	}
}

func TestMirroredTopicName(t *testing.T) {
	if got := MirroredTopicName("us-east", TopicRawMistTriggers); got != "us-east.analytics.raw_mist_triggers" {
		t.Fatalf("MirroredTopicName = %q", got)
	}
}

func TestMirrorExcludePatternMatchesOnlyMirroredCopies(t *testing.T) {
	pattern := MirrorExcludePattern([]string{"us-east", " eu-west ", "", "us-east"})
	if pattern != `^(eu-west|us-east)\..*` {
		t.Fatalf("MirrorExcludePattern = %q", pattern)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("pattern does not compile: %v", err)
	}
	for topic, excluded := range map[string]bool{
		"us-east.analytics_events":            true,
		"eu-west.us-east.service_events":      true,
		"analytics_events":                    false,
		"analytics.raw_mist_triggers":         false,
		"us-eastanalytics_events":             false,
		"ap-tokyo.analytics_events":           false,
		"billing.usage_reports":               false,
		"eu-west.analytics.raw_mist_triggers": true,
	} {
		if re.MatchString(topic) != excluded {
			t.Fatalf("pattern %q match(%q) = %v, want %v", pattern, topic, !excluded, excluded)
		}
	}

	if got := MirrorExcludePattern(nil); got != "" {
		t.Fatalf("MirrorExcludePattern(nil) = %q, want empty", got)
	}
}
