package provisioner

import "testing"

func TestSanitizeKafkaTopicsAcceptsStringMapConfig(t *testing.T) {
	topics := []map[string]any{
		{
			"name":               "analytics_events",
			"partitions":         6,
			"replication_factor": 3,
			"config": map[string]string{
				"cleanup.policy": "compact",
			},
		},
	}

	got, err := sanitizeKafkaTopics(topics)
	if err != nil {
		t.Fatalf("sanitizeKafkaTopics returned error: %v", err)
	}

	cfg, ok := got[0]["config"].(map[string]any)
	if !ok {
		t.Fatalf("sanitized config has type %T, want map[string]any", got[0]["config"])
	}
	if cfg["cleanup.policy"] != "compact" {
		t.Fatalf("sanitized config cleanup.policy = %v, want compact", cfg["cleanup.policy"])
	}
}

func TestSanitizeKafkaTopicsDropsNilAndEmptyConfig(t *testing.T) {
	topics := []map[string]any{
		{
			"name":               "service_events",
			"partitions":         3,
			"replication_factor": 3,
			"config":             map[string]any{},
			"retention.ms":       nil,
		},
	}

	got, err := sanitizeKafkaTopics(topics)
	if err != nil {
		t.Fatalf("sanitizeKafkaTopics returned error: %v", err)
	}

	if _, ok := got[0]["config"]; ok {
		t.Fatalf("sanitized topic unexpectedly kept empty config")
	}
	if _, ok := got[0]["retention.ms"]; ok {
		t.Fatalf("sanitized topic unexpectedly kept nil field")
	}
}
