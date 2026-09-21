// Package appconfig holds the typed startup configuration of the Signalman
// realtime hub. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import (
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Signalman is the startup configuration of the Signalman realtime hub. Every
// field applies at startup; a SIGHUP env-file reload updates the process
// environment but does not change a running instance.
//
//configref:service signalman cmd=cmd/signalman
type Signalman struct {
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.ServiceAuth
	config.GRPCTLS
	config.Registration

	KafkaBrokers            []string `env:"KAFKA_BROKERS" required:"true" desc:"Comma-separated Kafka bootstrap brokers for the event consumer and the DLQ producer." introduced:"v0.3.0"`
	KafkaClusterID          string   `env:"KAFKA_CLUSTER_ID" required:"true" desc:"Kafka cluster identifier attached to the consumer and DLQ producer clients." introduced:"v0.3.0"`
	KafkaGroupID            string   `env:"KAFKA_GROUP_ID" default:"signalman-group" desc:"Kafka consumer group. Each replica needs its own group so every replica receives every event for fan-out." introduced:"v0.3.0"`
	KafkaClientID           string   `env:"KAFKA_CLIENT_ID" default:"signalman" desc:"Kafka client ID of the event consumer." introduced:"v0.3.0"`
	KafkaConsumeResetOffset string   `env:"KAFKA_CONSUME_RESET_OFFSET" default:"latest" desc:"Where a consumer group without committed offsets starts. latest (case-insensitive) skips retained history; any other value starts from the Kafka client default." introduced:"v0.3.0"`
	AnalyticsTopic          string   `env:"ANALYTICS_KAFKA_TOPIC" default:"analytics_events" desc:"Kafka topic of analytics events fanned out to subscribers." introduced:"v0.3.0"`
	ServiceEventsTopic      string   `env:"SERVICE_EVENTS_KAFKA_TOPIC" default:"service_events" desc:"Kafka topic of service events fanned out to subscribers." introduced:"v0.3.0"`
	DLQTopic                string   `env:"DECKLOG_DLQ_KAFKA_TOPIC" default:"decklog_events_dlq" desc:"Kafka topic that receives events whose fan-out handler failed." introduced:"v0.3.0"`
	MirrorRegionPrefixes    []string `env:"MIRROR_REGION_PREFIXES" desc:"Comma-separated MirrorMaker2 source cluster aliases whose mirrored analytics and service event topics are also consumed. Empty consumes only local topics." introduced:"v0.3.0"`

	MaxConnectionsPerTenant int `env:"SIGNALMAN_MAX_CONNECTIONS_PER_TENANT" default:"0" desc:"Maximum concurrent Subscribe streams per tenant. 0 or less disables the limit." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address used for service registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"SIGNALMAN_HOST" default:"signalman" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
}

// ResetOffsetLatest reports whether a new consumer group starts at the latest offset.
func (c *Signalman) ResetOffsetLatest() bool {
	return strings.EqualFold(c.KafkaConsumeResetOffset, "latest")
}
