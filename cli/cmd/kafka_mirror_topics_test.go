package cmd

import (
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestMirrorLinkTopicsSelectsSetByDirection(t *testing.T) {
	intoAggregator := mirrorLinkTopics(inventory.KafkaMirrorLink{Source: "us-east", Target: "eu-west"}, "eu-west")
	if !slices.Equal(intoAggregator, topology.RegionalToAggregatorTopics()) {
		t.Fatalf("link into aggregator topics = %v, want %v", intoAggregator, topology.RegionalToAggregatorTopics())
	}
	if !slices.Contains(intoAggregator, topology.TopicRawMistTriggers) {
		t.Fatalf("link into aggregator omits the raw final-trigger journal: %v", intoAggregator)
	}

	outOfAggregator := mirrorLinkTopics(inventory.KafkaMirrorLink{Source: "eu-west", Target: "us-east"}, "eu-west")
	if !slices.Equal(outOfAggregator, topology.AggregatorToRegionalTopics()) {
		t.Fatalf("link out of aggregator topics = %v, want realtime set", outOfAggregator)
	}

	betweenRegions := mirrorLinkTopics(inventory.KafkaMirrorLink{Source: "us-east", Target: "ap-south"}, "eu-west")
	if !slices.Equal(betweenRegions, topology.AggregatorToRegionalTopics()) {
		t.Fatalf("regional-to-regional link topics = %v, want realtime set", betweenRegions)
	}

	override := mirrorLinkTopics(inventory.KafkaMirrorLink{Source: "us-east", Target: "eu-west", Topics: []string{"analytics_events"}}, "eu-west")
	if !slices.Equal(override, []string{"analytics_events"}) {
		t.Fatalf("explicit link topics = %v, want override", override)
	}
}

func TestMirroredSourceAliasesFollowLinksIntoCluster(t *testing.T) {
	manifest := threeRegionKafkaManifest()
	manifest.Infrastructure.Kafka.MirrorMaker = &inventory.KafkaMirrorMakerConfig{
		Enabled: true,
		Links: []inventory.KafkaMirrorLink{
			{Source: "us-east", Target: "eu-west"},
			{Source: "ap-south", Target: "eu-west"},
			{Source: "eu-west", Target: "us-east"},
			{Source: "ap-south", Target: "us-east"},
		},
	}

	if got := mirroredSourceAliases(manifest, "eu-west"); !slices.Equal(got, []string{"ap-south", "us-east"}) {
		t.Fatalf("eu-west prefixes = %v", got)
	}
	if got := mirroredSourceAliases(manifest, "us-east"); !slices.Equal(got, []string{"ap-south", "eu-west"}) {
		t.Fatalf("us-east prefixes = %v", got)
	}
	if got := mirroredSourceAliases(manifest, "ap-south"); len(got) != 0 {
		t.Fatalf("ap-south prefixes = %v, want none without links into it", got)
	}

	manifest.Infrastructure.Kafka.MirrorMaker.Enabled = false
	if got := mirroredSourceAliases(manifest, "eu-west"); len(got) != 0 {
		t.Fatalf("disabled MirrorMaker prefixes = %v, want none", got)
	}
}
