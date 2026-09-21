package appconfig

import (
	"slices"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadBosun(values map[string]string) (*Bosun, error) {
	return config.Load[Bosun](config.Options{
		Service: "bosun",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
}

func requiredBosun() map[string]string {
	return map[string]string{
		"DATABASE_URL":               "postgres://bosun@db:5432/bosun",
		"SERVICE_TOKEN":              "token",
		"JWT_SECRET":                 "jwt",
		"BOSUN_FIELD_ENCRYPTION_KEY": "bosun-field-key-0123456789",
		"QUARTERMASTER_GRPC_ADDR":    "quartermaster:19002",
		"PURSER_GRPC_ADDR":           "purser:19003",
		"DECKLOG_GRPC_ADDR":          "decklog:18006",
		"KAFKA_BROKERS":              "kafka-1:9092, kafka-2:9092",
	}
}

func TestBosunDefaults(t *testing.T) {
	cfg, err := loadBosun(requiredBosun())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListenPort() != "18013" || cfg.GRPCPort != "19009" {
		t.Fatalf("ports = http %q grpc %q", cfg.HTTPListenPort(), cfg.GRPCPort)
	}
	if cfg.AdvertiseHost != "bosun" || cfg.KafkaClusterID != "local" || cfg.DLQTopic != "decklog_events_dlq" {
		t.Fatalf("defaults: %+v", cfg)
	}
	if !slices.Equal(cfg.KafkaBrokers, []string{"kafka-1:9092", "kafka-2:9092"}) || len(cfg.MirrorRegionPrefixes) != 0 {
		t.Fatalf("kafka = %v, prefixes %v", cfg.KafkaBrokers, cfg.MirrorRegionPrefixes)
	}
	if cfg.FieldEncryptionKeyID != "primary" || cfg.SMTPHost != "" || cfg.SMTPPort != "587" {
		t.Fatalf("defaults: %+v", cfg)
	}
	ring, err := cfg.FieldKeyring()
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := ring.Encrypt("whsec_x")
	if err != nil || !strings.HasPrefix(ciphertext, "enc:v3:primary:") {
		t.Fatalf("ciphertext %q, %v", ciphertext, err)
	}
	values := requiredBosun()
	values["PORT"] = "9999"
	cfg, err = loadBosun(values)
	if err != nil || cfg.HTTPListenPort() != "9999" {
		t.Fatalf("PORT override = %q, %v", cfg.HTTPListenPort(), err)
	}
}

func TestBosunRequiredKeys(t *testing.T) {
	for key := range requiredBosun() {
		values := requiredBosun()
		delete(values, key)
		if _, err := loadBosun(values); err == nil {
			t.Errorf("missing %s loaded", key)
		}
	}
}

func TestBosunRejectsAShortFieldKey(t *testing.T) {
	values := requiredBosun()
	values["BOSUN_FIELD_ENCRYPTION_KEY"] = "short"
	if _, err := loadBosun(values); err == nil {
		t.Fatal("a field key under 16 bytes loaded")
	}
	values = requiredBosun()
	values["BOSUN_FIELD_ENCRYPTION_PREVIOUS_KEYS"] = "not json"
	if _, err := loadBosun(values); err == nil {
		t.Fatal("unparseable previous keys loaded")
	}
}
