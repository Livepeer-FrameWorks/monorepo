package provisioner

import "testing"

func TestKafkaInternalTopicHADerivesThreeBrokerDefaults(t *testing.T) {
	got := kafkaInternalTopicHA(map[string]any{"broker_count": 3})

	if got.minISR != 2 {
		t.Fatalf("minISR = %d, want 2", got.minISR)
	}
	if got.offsetsRF != 3 {
		t.Fatalf("offsetsRF = %d, want 3", got.offsetsRF)
	}
	if got.transactionRF != 3 {
		t.Fatalf("transactionRF = %d, want 3", got.transactionRF)
	}
	if got.transactionMinISR != 2 {
		t.Fatalf("transactionMinISR = %d, want 2", got.transactionMinISR)
	}
}

func TestKafkaInternalTopicHACapsOverridesToBrokerCount(t *testing.T) {
	got := kafkaInternalTopicHA(map[string]any{
		"broker_count":                             2,
		"offsets_topic_replication_factor":         3,
		"transaction_state_log_replication_factor": 3,
		"transaction_state_log_min_isr":            3,
	})

	if got.offsetsRF != 2 {
		t.Fatalf("offsetsRF = %d, want 2", got.offsetsRF)
	}
	if got.transactionRF != 2 {
		t.Fatalf("transactionRF = %d, want 2", got.transactionRF)
	}
	if got.transactionMinISR != 2 {
		t.Fatalf("transactionMinISR = %d, want 2", got.transactionMinISR)
	}
}

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
