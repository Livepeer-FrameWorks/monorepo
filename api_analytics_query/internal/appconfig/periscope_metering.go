package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// PeriscopeMetering is the startup configuration of the Periscope Metering
// worker. Every field applies at startup.
//
//configref:service periscope-metering cmd=cmd/periscope-metering
type PeriscopeMetering struct {
	config.HTTPListen
	config.HTTPRuntime
	config.Logging
	config.SystemTenant
	config.Postgres
	config.ClickHouse

	ServiceToken string   `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service token for Quartermaster tenant lookups. Also guards the /debug endpoints." introduced:"v0.3.0"`
	KafkaBrokers []string `env:"KAFKA_BROKERS" required:"true" desc:"Comma-separated Kafka bootstrap brokers that receive billing usage reports." introduced:"v0.3.0"`

	MeteringSourceID     string `env:"METERING_SOURCE_ID" required:"true" desc:"Durable identity of the metered dataset, recorded on leases, cursors, and usage reports. Lowercase letters, digits, dot, underscore, and dash, at most 128 characters." introduced:"v0.3.0"`
	MeteringSourceRegion string `env:"METERING_SOURCE_REGION" required:"true" desc:"Region of the metered dataset recorded on usage reports. Lowercase letters, digits, dot, underscore, and dash, at most 64 characters." introduced:"v0.3.0"`
	MeteringWorkerID     string `env:"METERING_WORKER_ID" desc:"Lease owner ID of this replica. Empty uses the host name plus a random UUID, so the owner changes on restart." introduced:"v0.3.0"`
	BillingKafkaTopic    string `env:"BILLING_KAFKA_TOPIC" default:"billing.usage_reports" desc:"Kafka topic that usage reports are published to." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address used to resolve a tenant's primary cluster." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	GRPCTLSCAPath                  string `env:"GRPC_TLS_CA_PATH" desc:"CA bundle used to verify the Quartermaster gRPC server." introduced:"v0.3.0"`
	GRPCAllowInsecure              bool   `env:"GRPC_ALLOW_INSECURE" default:"false" desc:"Allows a plaintext Quartermaster connection when no TLS material is configured. Development only." introduced:"v0.3.0"`
}
