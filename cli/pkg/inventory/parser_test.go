package inventory

import "testing"

func TestManifestValidateKafkaRequiresZookeeperConnect(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {Address: "10.0.0.10", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled: true,
				Brokers: []KafkaBroker{{Host: "broker-1", ID: 1}},
			},
		},
	}

	if err := manifest.Validate(); err == nil {
		t.Fatal("expected validation error for missing zookeeper_connect")
	}
}

func TestManifestValidateKafkaWithZookeeperEnsemble(t *testing.T) {
	manifest := &Manifest{
		Version: "1",
		Type:    "cluster",
		Hosts: map[string]Host{
			"broker-1": {Address: "10.0.0.10", User: "root"},
			"zk-1":     {Address: "10.0.0.20", User: "root"},
		},
		Infrastructure: InfrastructureConfig{
			Kafka: &KafkaConfig{
				Enabled: true,
				Brokers: []KafkaBroker{{Host: "broker-1", ID: 1}},
			},
			Zookeeper: &ZookeeperConfig{
				Enabled:  true,
				Ensemble: []ZookeeperNode{{Host: "zk-1", ID: 1}},
			},
		},
	}

	if err := manifest.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
