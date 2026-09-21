// Package appconfig holds the typed startup configuration of the Decklog event
// ingress. scripts/configref generates the operator configuration reference
// from these structs.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// Decklog is the startup configuration of the Decklog event ingress. Every
// field applies at startup; a SIGHUP env-file reload updates the process
// environment but does not change a running instance.
//
//configref:service decklog cmd=cmd/decklog
type Decklog struct {
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GRPCTLS
	config.Registration

	GRPCPort    string `env:"PORT" default:"@servicedefs.grpc_port" desc:"TCP port for the gRPC listener that receives events. Also the port registered with Quartermaster." introduced:"v0.3.0"`
	MetricsPort string `env:"DECKLOG_METRICS_PORT" default:"18026" desc:"TCP port for the HTTP listener that serves health, readiness, and metrics." introduced:"v0.3.0"`

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Service token required on incoming gRPC calls and sent on Quartermaster calls." introduced:"v0.3.0"`

	KafkaBrokers       []string `env:"KAFKA_BROKERS" required:"true" desc:"Comma-separated Kafka bootstrap brokers that receive published events." introduced:"v0.3.0"`
	KafkaClusterID     string   `env:"KAFKA_CLUSTER_ID" required:"true" desc:"Kafka cluster identifier attached to the producer client." introduced:"v0.3.0"`
	AnalyticsTopic     string   `env:"ANALYTICS_KAFKA_TOPIC" default:"analytics_events" desc:"Default Kafka topic for analytics events." introduced:"v0.3.0"`
	ServiceEventsTopic string   `env:"SERVICE_EVENTS_KAFKA_TOPIC" default:"service_events" desc:"Kafka topic for service events." introduced:"v0.3.0"`
	RawTriggersTopic   string   `env:"DECKLOG_RAW_TRIGGERS_TOPIC" desc:"Kafka topic that journals the original MistTrigger envelope of final and accounting triggers. Empty uses analytics.raw_mist_triggers; - disables the journal." introduced:"v0.3.0"`

	Region       string `env:"REGION" desc:"Region label backfilled onto events that arrive without a source region. Takes precedence over REGION_ID and DECKLOG_SOURCE_REGION." introduced:"v0.3.0"`
	RegionID     string `env:"REGION_ID" desc:"Region label backfilled onto events without a source region when REGION is empty." introduced:"v0.3.0"`
	SourceRegion string `env:"DECKLOG_SOURCE_REGION" desc:"Region label backfilled onto events without a source region when REGION and REGION_ID are empty." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address used for service registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"DECKLOG_HOST" default:"decklog" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
}

// BackfillRegion resolves the instance region label from REGION, then
// REGION_ID, then DECKLOG_SOURCE_REGION.
func (c *Decklog) BackfillRegion() string {
	for _, v := range []string{c.Region, c.RegionID, c.SourceRegion} {
		if v != "" {
			return v
		}
	}
	return ""
}
