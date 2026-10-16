package cmd

import (
	"maps"
	"slices"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"

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

// MirrorMaker2's periodic topic config sync on Kafka 4.0-4.2 logged "Could not
// alter configuration of topic" for every mirrored copy every 10 minutes, and
// the copies' config only followed the source through that sync. The kafka
// role of the target cluster declares the copies instead, with the source's
// declared config, so a retention change converges on every copy.
func TestKafkaTaskDeclaresMirroredTopicCopies(t *testing.T) {
	manifest := threeRegionKafkaManifest()
	kafka := manifest.Infrastructure.Kafka
	declare := func(names []string) []inventory.KafkaTopic {
		var topics []inventory.KafkaTopic
		for _, name := range names {
			spec, _ := topology.CanonicalTopic(name)
			partitions := spec.Partitions
			if partitions == 0 {
				partitions = 3
			}
			topics = append(topics, inventory.KafkaTopic{Name: name, Partitions: partitions, ReplicationFactor: 3, Config: spec.Config})
		}
		return topics
	}
	kafka.Topics = declare(topology.ClusterTopics(true))
	for i := range kafka.Regional {
		kafka.Regional[i].Topics = declare(topology.ClusterTopics(false))
	}
	kafka.MirrorMaker = &inventory.KafkaMirrorMakerConfig{
		Enabled: true,
		Links: []inventory.KafkaMirrorLink{
			{Source: "us-east", Target: "eu-west", Hosts: []string{"regional-eu-1"}},
			{Source: "eu-west", Target: "us-east", Hosts: []string{"regional-us-1"}},
		},
	}

	topicsOf := func(clusterID, host string) map[string]map[string]any {
		t.Helper()
		task := &orchestrator.Task{Name: "kafka-" + host, Type: "kafka", ServiceID: "kafka", InstanceID: "1", Host: host, ClusterID: clusterID, Phase: orchestrator.PhaseInfrastructure}
		config, err := buildTaskConfig(task, manifest, map[string]any{}, false, "", map[string]string{}, nil, nil)
		if err != nil {
			t.Fatalf("buildTaskConfig(%s): %v", clusterID, err)
		}
		topics, _ := config.Metadata["topics"].([]map[string]any)
		byName := map[string]map[string]any{}
		for _, topic := range topics {
			byName[topic["name"].(string)] = topic
		}
		return byName
	}

	aggregator := topicsOf("eu-west", "regional-eu-1")
	for _, name := range topology.RegionalToAggregatorTopics() {
		copyName := topology.MirroredTopicName("us-east", name)
		topic, ok := aggregator[copyName]
		if !ok {
			t.Fatalf("aggregator kafka task does not declare mirrored copy %q; declared %v", copyName, slices.Sorted(maps.Keys(aggregator)))
		}
		spec, _ := topology.CanonicalTopic(name)
		cfg, _ := topic["config"].(map[string]string)
		if cfg["retention.ms"] != spec.Config["retention.ms"] {
			t.Fatalf("%s retention.ms = %q, want the source's %q", copyName, cfg["retention.ms"], spec.Config["retention.ms"])
		}
		if spec.Partitions > 0 && topic["partitions"] != spec.Partitions {
			t.Fatalf("%s partitions = %v, want %d", copyName, topic["partitions"], spec.Partitions)
		}
		if topic["replication_factor"] != 3 {
			t.Fatalf("%s replication_factor = %v, want 3", copyName, topic["replication_factor"])
		}
	}
	if _, ok := aggregator[topology.MirroredTopicName("ap-south", topology.TopicServiceEvents)]; ok {
		t.Fatal("aggregator declares a copy for ap-south, which has no link into it")
	}

	regional := topicsOf("us-east", "regional-us-1")
	for _, name := range topology.AggregatorToRegionalTopics() {
		if _, ok := regional[topology.MirroredTopicName("eu-west", name)]; !ok {
			t.Fatalf("us-east kafka task does not declare mirrored copy of %q", name)
		}
	}
	if _, ok := regional[topology.MirroredTopicName("eu-west", topology.TopicBillingUsageReports)]; ok {
		t.Fatal("us-east declares a copy of a topic its inbound link does not carry")
	}

	kafka.MirrorMaker.Enabled = false
	if _, ok := topicsOf("eu-west", "regional-eu-1")[topology.MirroredTopicName("us-east", topology.TopicServiceEvents)]; ok {
		t.Fatal("disabled MirrorMaker still declares mirrored copies")
	}
}
