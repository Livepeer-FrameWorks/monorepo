package appconfig

import (
	"reflect"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadSignalman(t *testing.T, extra map[string]string) *Signalman {
	t.Helper()
	values := map[string]string{
		"KAFKA_BROKERS":    "kafka:9092",
		"KAFKA_CLUSTER_ID": "cluster-a",
		"SERVICE_TOKEN":    "token",
		"JWT_SECRET":       "jwt",
	}
	for k, v := range extra {
		values[k] = v
	}
	cfg, err := config.Load[Signalman](config.Options{
		Service: "signalman",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestSignalmanDefaults(t *testing.T) {
	cfg := loadSignalman(t, nil)
	if cfg.HTTPListen.Port != "18009" || cfg.GRPCListen.Port != "19005" {
		t.Fatalf("ports = http %q grpc %q", cfg.HTTPListen.Port, cfg.GRPCListen.Port)
	}
	if cfg.KafkaGroupID != "signalman-group" || cfg.KafkaClientID != "signalman" || cfg.MaxConnectionsPerTenant != 0 {
		t.Fatalf("kafka defaults = %q %q limit %d", cfg.KafkaGroupID, cfg.KafkaClientID, cfg.MaxConnectionsPerTenant)
	}
	if !cfg.ResetOffsetLatest() {
		t.Fatal("reset offset defaults to latest")
	}
}

func TestSignalmanResetOffsetAndMirrors(t *testing.T) {
	cfg := loadSignalman(t, map[string]string{
		"KAFKA_CONSUME_RESET_OFFSET": "earliest",
		"MIRROR_REGION_PREFIXES":     " us-east, ,ap-tokyo ",
	})
	if cfg.ResetOffsetLatest() {
		t.Fatal("earliest must not reset to latest")
	}
	if want := []string{"us-east", "ap-tokyo"}; !reflect.DeepEqual(cfg.MirrorRegionPrefixes, want) {
		t.Fatalf("mirror prefixes = %q", cfg.MirrorRegionPrefixes)
	}
	if !loadSignalman(t, map[string]string{"KAFKA_CONSUME_RESET_OFFSET": "LATEST"}).ResetOffsetLatest() {
		t.Fatal("LATEST matches case-insensitively")
	}
}
