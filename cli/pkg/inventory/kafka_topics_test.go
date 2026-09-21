package inventory

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func kafkaTopicManifest(brokers int, topics []KafkaTopic, regional []KafkaTopic) *Manifest {
	hosts := map[string]Host{}
	var brokerList []KafkaBroker
	for i := 1; i <= brokers; i++ {
		name := "broker-" + string(rune('0'+i))
		hosts[name] = Host{ExternalIP: "10.0.0.1" + string(rune('0'+i)), User: "root"}
		brokerList = append(brokerList, KafkaBroker{Host: name, ID: i})
	}
	kafka := &KafkaConfig{Enabled: true, ClusterID: "kraft-eu", RegionID: "eu-west", Brokers: brokerList, Topics: topics}
	if regional != nil {
		hosts["broker-us"] = Host{ExternalIP: "10.0.1.10", User: "root"}
		kafka.Regional = []RegionalKafkaCluster{{
			RegionID: "us-east", ClusterID: "kraft-us",
			Brokers: []KafkaBroker{{Host: "broker-us", ID: 11}}, Topics: regional,
		}}
	}
	return &Manifest{Version: "1", Type: "cluster", Hosts: hosts, Infrastructure: InfrastructureConfig{Kafka: kafka}}
}

// canonicalManifestTopic is the manifest entry an operator writes for a
// canonical topic on a cluster with the given broker count.
func canonicalManifestTopic(t *testing.T, name string, brokers int) KafkaTopic {
	t.Helper()
	spec, ok := topology.CanonicalTopic(name)
	if !ok {
		t.Fatalf("%s is not canonical", name)
	}
	topic := KafkaTopic{Name: name, Partitions: 3, ReplicationFactor: min(3, brokers), Config: spec.Config}
	if spec.Partitions > 0 {
		topic.Partitions = spec.Partitions
	}
	return topic
}

// requiredManifestTopics is the full topic list a cluster of the given role
// must declare.
func requiredManifestTopics(t *testing.T, brokers int, aggregator bool) []KafkaTopic {
	t.Helper()
	var topics []KafkaTopic
	for _, name := range topology.ClusterTopics(aggregator) {
		topics = append(topics, canonicalManifestTopic(t, name, brokers))
	}
	return topics
}

// replaceTopic returns topics with the entry named like replacement swapped
// for it.
func replaceTopic(topics []KafkaTopic, replacement KafkaTopic) []KafkaTopic {
	out := make([]KafkaTopic, 0, len(topics))
	for _, topic := range topics {
		if topic.Name == replacement.Name {
			out = append(out, replacement)
			continue
		}
		out = append(out, topic)
	}
	return out
}

func withoutTopic(topics []KafkaTopic, name string) []KafkaTopic {
	out := make([]KafkaTopic, 0, len(topics))
	for _, topic := range topics {
		if topic.Name != name {
			out = append(out, topic)
		}
	}
	return out
}

func TestManifestValidateAcceptsEveryCanonicalTopic(t *testing.T) {
	var topics []KafkaTopic
	for _, spec := range topology.CanonicalTopics() {
		topics = append(topics, canonicalManifestTopic(t, spec.Name, 3))
	}
	topics = append(topics, KafkaTopic{Name: "operator.custom", Partitions: 1, ReplicationFactor: 1, Config: map[string]string{"retention.ms": "3600000"}})
	if err := kafkaTopicManifest(3, topics, nil).Validate(); err != nil {
		t.Fatalf("canonical topics rejected: %v", err)
	}
	if err := kafkaTopicManifest(1, requiredManifestTopics(t, 1, true), nil).Validate(); err != nil {
		t.Fatalf("single-broker topics rejected: %v", err)
	}
}

func TestManifestValidateRejectsTopicWithoutCanonicalRetention(t *testing.T) {
	withConfig := func(name string, mutate func(*KafkaTopic)) []KafkaTopic {
		topic := canonicalManifestTopic(t, name, 3)
		cfg := map[string]string{}
		for k, v := range topic.Config {
			cfg[k] = v
		}
		topic.Config = cfg
		mutate(&topic)
		return replaceTopic(requiredManifestTopics(t, 3, true), topic)
	}
	cases := map[string]struct {
		topics []KafkaTopic
		want   string
	}{
		"missing retention": {withConfig(topology.TopicBillingUsageReports, func(k *KafkaTopic) { delete(k.Config, "retention.ms") }), "config.retention.ms is required"},
		"non-numeric":       {withConfig(topology.TopicServiceEvents, func(k *KafkaTopic) { k.Config["retention.ms"] = "180d" }), "positive number of milliseconds"},
		"broker default":    {withConfig(topology.TopicServiceEvents, func(k *KafkaTopic) { k.Config["retention.ms"] = "604800000" }), `config.retention.ms must be "15552000000"`},
		"domain compaction": {withConfig(topology.TopicDomainEvents, func(k *KafkaTopic) { k.Config["cleanup.policy"] = "compact" }), `config.cleanup.policy must be "delete"`},
		"domain partitions": {withConfig(topology.TopicDomainEvents, func(k *KafkaTopic) { k.Partitions = 6 }), "partitions must be 12"},
		"domain rf":         {withConfig(topology.TopicDomainEvents, func(k *KafkaTopic) { k.ReplicationFactor = 2 }), "replication_factor must be 3"},
		"custom topic": {
			append(requiredManifestTopics(t, 3, true), KafkaTopic{Name: "operator.custom", Partitions: 1, ReplicationFactor: 1}),
			"config.retention.ms is required",
		},
	}
	for name, tc := range cases {
		err := kafkaTopicManifest(3, tc.topics, nil).Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: error = %v, want %q", name, err, tc.want)
		}
	}
}

func TestManifestValidateChecksRegionalClusterTopics(t *testing.T) {
	aggregator := requiredManifestTopics(t, 3, true)
	good := requiredManifestTopics(t, 1, false)
	if err := kafkaTopicManifest(3, aggregator, good).Validate(); err != nil {
		t.Fatalf("regional canonical topics rejected: %v", err)
	}
	bad := replaceTopic(good, KafkaTopic{Name: topology.TopicDomainEvents, Partitions: 12, ReplicationFactor: 1})
	err := kafkaTopicManifest(3, aggregator, bad).Validate()
	if err == nil || !strings.Contains(err.Error(), "kafka.regional[0] (us-east)") || !strings.Contains(err.Error(), "retention.ms is required") {
		t.Fatalf("regional topic without retention: %v", err)
	}
}

// A cluster that omits a canonical topic is refused with the entry to add,
// both at the top level and in a regional block. The aggregator also needs
// the unmirrored Lookout topic; a regional cluster does not.
func TestManifestValidateRequiresEveryCanonicalTopicPerCluster(t *testing.T) {
	aggregator := requiredManifestTopics(t, 3, true)
	regional := requiredManifestTopics(t, 1, false)
	for _, spec := range topology.CanonicalTopics() {
		name := spec.Name
		err := kafkaTopicManifest(3, withoutTopic(aggregator, name), regional).Validate()
		if err == nil || !strings.Contains(err.Error(), `required topic "`+name+`" is missing`) || !strings.Contains(err.Error(), "  - name: "+name+"\n") {
			t.Fatalf("aggregator without %s: %v", name, err)
		}
	}
	for _, name := range append(topology.RegionalToAggregatorTopics(), topology.AggregatorToRegionalTopics()...) {
		err := kafkaTopicManifest(3, aggregator, withoutTopic(regional, name)).Validate()
		if err == nil || !strings.Contains(err.Error(), "kafka.regional[0] (us-east)") || !strings.Contains(err.Error(), `required topic "`+name+`" is missing`) {
			t.Fatalf("regional without %s: %v", name, err)
		}
	}
	err := kafkaTopicManifest(3, withoutTopic(aggregator, topology.TopicDomainEvents), regional).Validate()
	want := "  - name: domain.events\n    partitions: 12\n    replication_factor: 3\n    config:\n      cleanup.policy: \"delete\"\n      retention.ms: \"15552000000\""
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("domain.events YAML hint = %v, want %q", err, want)
	}
	if strings.Contains(strings.Join(topology.ClusterTopics(false), ","), topology.TopicLookoutIncidents) {
		t.Fatal("regional clusters must not require lookout.incidents")
	}
}
