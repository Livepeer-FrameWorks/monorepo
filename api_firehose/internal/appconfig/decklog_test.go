package appconfig

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func loadDecklog(values map[string]string) (*Decklog, error) {
	return config.Load[Decklog](config.Options{
		Service: "decklog",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
}

var decklogRequired = map[string]string{
	"KAFKA_BROKERS":    "kafka-1:9092, kafka-2:9092",
	"KAFKA_CLUSTER_ID": "cluster-a",
	"SERVICE_TOKEN":    "token",
}

func TestDecklogDefaults(t *testing.T) {
	cfg, err := loadDecklog(decklogRequired)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GRPCPort != "18006" || cfg.MetricsPort != "18026" {
		t.Fatalf("ports = grpc %q metrics %q", cfg.GRPCPort, cfg.MetricsPort)
	}
	if want := []string{"kafka-1:9092", "kafka-2:9092"}; !reflect.DeepEqual(cfg.KafkaBrokers, want) {
		t.Fatalf("brokers = %q", cfg.KafkaBrokers)
	}
	if cfg.AnalyticsTopic != topology.TopicAnalyticsEvents || cfg.ServiceEventsTopic != topology.TopicServiceEvents || cfg.RawTriggersTopic != "" {
		t.Fatalf("topics = %q %q %q", cfg.AnalyticsTopic, cfg.ServiceEventsTopic, cfg.RawTriggersTopic)
	}
}

func TestDecklogRequiresKafkaAndServiceToken(t *testing.T) {
	_, err := loadDecklog(nil)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	for _, key := range []string{"KAFKA_BROKERS", "KAFKA_CLUSTER_ID", "SERVICE_TOKEN"} {
		if !slices.Contains(loadErr.Missing, key) {
			t.Fatalf("missing = %v, want %s", loadErr.Missing, key)
		}
	}
}

func TestDecklogBackfillRegionPrecedence(t *testing.T) {
	values := map[string]string{"DECKLOG_SOURCE_REGION": "source"}
	for k, v := range decklogRequired {
		values[k] = v
	}
	check := func(want string) {
		t.Helper()
		cfg, err := loadDecklog(values)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got := cfg.BackfillRegion(); got != want {
			t.Fatalf("BackfillRegion = %q, want %q", got, want)
		}
	}
	check("source")
	values["REGION_ID"] = "region-id"
	check("region-id")
	values["REGION"] = "region"
	check("region")
}
