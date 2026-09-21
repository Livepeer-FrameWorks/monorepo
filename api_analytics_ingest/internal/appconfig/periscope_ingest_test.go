package appconfig

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func loadIngest(values map[string]string) (*PeriscopeIngest, error) {
	return config.Load[PeriscopeIngest](config.Options{
		Service: "periscope-ingest",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
}

func requiredIngest() map[string]string {
	return map[string]string{
		"DATABASE_URL":        "postgres://periscope@db/periscope",
		"CLICKHOUSE_ADDR":     "ch-1:9000, ch-2:9000",
		"CLICKHOUSE_DB":       "periscope",
		"CLICKHOUSE_USER":     "periscope",
		"CLICKHOUSE_PASSWORD": "secret",
		"KAFKA_BROKERS":       "kafka:9092",
		"KAFKA_CLUSTER_ID":    "cluster-a",
		"SERVICE_TOKEN":       "token",
	}
}

func TestPeriscopeIngestDefaults(t *testing.T) {
	cfg, err := loadIngest(requiredIngest())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "18005" {
		t.Fatalf("PORT = %q", cfg.Port)
	}
	if want := []string{"ch-1:9000", "ch-2:9000"}; !reflect.DeepEqual(cfg.Addr, want) {
		t.Fatalf("clickhouse addr = %q", cfg.Addr)
	}
	if cfg.KafkaGroupID != "periscope-ingest" || cfg.DLQTopic != topology.TopicDecklogDLQ || cfg.RawTriggersTopic != topology.TopicRawMistTriggers {
		t.Fatalf("kafka defaults = %q %q %q", cfg.KafkaGroupID, cfg.DLQTopic, cfg.RawTriggersTopic)
	}
	if cfg.AnalyticsTopic != topology.TopicAnalyticsEvents || cfg.ServiceEventsTopic != topology.TopicServiceEvents {
		t.Fatalf("topic defaults = %q %q", cfg.AnalyticsTopic, cfg.ServiceEventsTopic)
	}
	if len(cfg.MirrorRegionPrefixes) != 0 {
		t.Fatalf("mirrors = %q", cfg.MirrorRegionPrefixes)
	}
}

func TestPeriscopeIngestRequiredKeys(t *testing.T) {
	_, err := loadIngest(nil)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	for key := range requiredIngest() {
		if !slices.Contains(loadErr.Missing, key) {
			t.Fatalf("missing = %v, want %s", loadErr.Missing, key)
		}
	}
}

func TestPeriscopeIngestMirrorRegionPrefixes(t *testing.T) {
	values := requiredIngest()
	values["MIRROR_REGION_PREFIXES"] = " us-east, ,ap-tokyo,"
	cfg, err := loadIngest(values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []string{"us-east", "ap-tokyo"}; !reflect.DeepEqual(cfg.MirrorRegionPrefixes, want) {
		t.Fatalf("mirror prefixes = %q, want %q", cfg.MirrorRegionPrefixes, want)
	}
}

func TestPeriscopeIngestRawTriggersDashIsTrimmed(t *testing.T) {
	values := requiredIngest()
	values["RAW_MIST_TRIGGERS_KAFKA_TOPIC"] = " - "
	cfg, err := loadIngest(values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RawTriggersTopic != "-" {
		t.Fatalf("raw triggers topic = %q, want the - that disables consumption", cfg.RawTriggersTopic)
	}
}
