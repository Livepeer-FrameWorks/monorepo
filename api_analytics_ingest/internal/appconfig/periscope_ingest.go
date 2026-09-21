// Package appconfig holds the typed startup configuration of the Periscope
// Ingest service. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// PeriscopeIngest is the startup configuration of the Periscope Ingest
// service. Every field applies at startup; a SIGHUP env-file reload updates
// the process environment but does not change a running instance.
//
//configref:service periscope-ingest cmd=cmd/periscope
type PeriscopeIngest struct {
	config.HTTPListen
	config.HTTPRuntime
	config.Logging
	config.Postgres
	config.ClickHouse
	config.Registration
	config.GRPCClientTLS

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Service token sent on Quartermaster calls and required by the /debug endpoints." introduced:"v0.3.0"`

	KafkaBrokers         []string `env:"KAFKA_BROKERS" required:"true" desc:"Comma-separated Kafka bootstrap brokers for the event consumer and the DLQ producer." introduced:"v0.3.0"`
	KafkaClusterID       string   `env:"KAFKA_CLUSTER_ID" required:"true" desc:"Kafka cluster identifier attached to the consumer and DLQ producer clients." introduced:"v0.3.0"`
	KafkaGroupID         string   `env:"KAFKA_GROUP_ID" default:"periscope-ingest" desc:"Kafka consumer group shared by Periscope Ingest replicas." introduced:"v0.3.0"`
	KafkaClientID        string   `env:"KAFKA_CLIENT_ID" default:"periscope-ingest" desc:"Kafka client ID of the event consumer." introduced:"v0.3.0"`
	AnalyticsTopic       string   `env:"ANALYTICS_KAFKA_TOPIC" default:"analytics_events" desc:"Kafka topic of analytics events projected into ClickHouse." introduced:"v0.3.0"`
	ServiceEventsTopic   string   `env:"SERVICE_EVENTS_KAFKA_TOPIC" default:"service_events" desc:"Kafka topic of service events projected into ClickHouse." introduced:"v0.3.0"`
	DLQTopic             string   `env:"DECKLOG_DLQ_KAFKA_TOPIC" default:"decklog_events_dlq" desc:"Kafka topic that receives analytics and service events whose handler failed with a non-retryable error." introduced:"v0.3.0"`
	RawTriggersTopic     string   `env:"RAW_MIST_TRIGGERS_KAFKA_TOPIC" default:"analytics.raw_mist_triggers" desc:"Kafka topic of raw MistTrigger envelopes projected into raw_mist_triggers. - disables consumption on this instance." introduced:"v0.3.0"`
	MirrorRegionPrefixes []string `env:"MIRROR_REGION_PREFIXES" desc:"Comma-separated MirrorMaker2 source cluster aliases. The mirrored copy of every consumed topic, including the raw MistTrigger topic, is also consumed for each alias. Empty consumes only local topics." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address used for service registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"PERISCOPE_INGEST_HOST" default:"periscope-ingest" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`

	EnableHealthEndpoint bool `env:"ENABLE_HEALTH_ENDPOINT" default:"true" deprecated:"v0.3.0" desc:"Ignored. The HTTP listener that serves health, readiness, and metrics always runs." introduced:"v0.3.0"`
}
