// Package appconfig holds the typed configuration of Lookout.
// scripts/configref generates the operator configuration reference from these
// structs.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

// Lookout is the configuration of the Lookout incident service. The
// Alertmanager token, the operator notification destinations, the SMTP
// settings, and the email branding are read through a config.Live snapshot on
// every use, so they follow a SIGHUP env-file reload; every other field
// applies at startup.
//
//configref:service lookout cmd=cmd/lookout
type Lookout struct {
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.EmailBranding

	HTTPPort         string `env:"LOOKOUT_PORT" default:"@servicedefs.http_port" desc:"TCP port for the HTTP listener that serves the Alertmanager webhook, health, readiness, and metrics. PORT overrides it." introduced:"v0.3.8"`
	HTTPPortOverride string `env:"PORT" desc:"TCP port for the HTTP listener. Takes precedence over LOOKOUT_PORT when set." introduced:"v0.3.8"`
	GRPCPort         string `env:"LOOKOUT_GRPC_PORT" default:"@servicedefs.grpc_port" desc:"TCP port for the gRPC listener that serves the incident API. Also the port registered with Quartermaster." introduced:"v0.3.8"`
	AdvertiseHost    string `env:"LOOKOUT_HOST" default:"lookout" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.8"`
	Region           string `env:"REGION" desc:"Region label attached to incident events sent to Decklog." introduced:"v0.3.8"`

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Service token accepted on incoming gRPC calls and sent to Quartermaster and Decklog." introduced:"v0.3.8"`
	JWTSecret    string `env:"JWT_SECRET" secret:"true" desc:"HMAC secret that verifies user JWTs on incoming gRPC calls. Empty accepts only the service token." introduced:"v0.3.8"`

	AlertmanagerToken string `env:"LOOKOUT_ALERTMANAGER_TOKEN" required:"true" secret:"true" desc:"Bearer token Alertmanager presents on the /v1/alertmanager webhook. Re-read after an env-file reload." introduced:"v0.3.8"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" required:"true" desc:"Quartermaster gRPC address for cluster ownership lookups and service registration." introduced:"v0.3.8"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.8"`
	DecklogGRPCAddr                string `env:"DECKLOG_GRPC_ADDR" required:"true" desc:"Decklog gRPC address for realtime incident events." introduced:"v0.3.8"`
	DecklogGRPCTLSServerName       string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.8"`

	KafkaBrokers   []string `env:"KAFKA_BROKERS" required:"true" desc:"Comma-separated bootstrap brokers of the aggregator Kafka cluster. Lookout publishes the incident topic there and consumes the local service events topic." introduced:"v0.3.8"`
	KafkaClusterID string   `env:"KAFKA_CLUSTER_ID" default:"local" desc:"Kafka cluster identifier attached to the incident producer and the service events consumer." introduced:"v0.3.8"`

	NotifyEmailTo     []string `env:"LOOKOUT_NOTIFY_EMAIL_TO" desc:"Comma-separated operator addresses that receive critical incident emails. Empty disables the email channel. Re-read after an env-file reload." introduced:"v0.3.8"`
	SlackWebhookURL   string   `env:"LOOKOUT_SLACK_WEBHOOK_URL" secret:"true" desc:"Slack incoming webhook URL for incident and operator activity notifications. Empty disables the Slack channel. Re-read after an env-file reload." introduced:"v0.3.8"`
	DiscordWebhookURL string   `env:"LOOKOUT_DISCORD_WEBHOOK_URL" secret:"true" desc:"Discord webhook URL for incident and operator activity notifications. Empty disables the Discord channel. Re-read after an env-file reload." introduced:"v0.3.8"`

	SMTPHost          string `env:"SMTP_HOST" desc:"SMTP server host for incident emails. Empty fails every email delivery. Re-read after an env-file reload." introduced:"v0.3.8"`
	SMTPPort          string `env:"SMTP_PORT" default:"587" desc:"SMTP server port for incident emails. Re-read after an env-file reload." introduced:"v0.3.8"`
	SMTPUser          string `env:"SMTP_USER" desc:"SMTP user for incident emails. Re-read after an env-file reload." introduced:"v0.3.8"`
	SMTPPassword      string `env:"SMTP_PASSWORD" secret:"true" desc:"Password for SMTP_USER. Re-read after an env-file reload." introduced:"v0.3.8"`
	FromEmail         string `env:"FROM_EMAIL" default:"noreply@frameworks.network" desc:"Sender address for incident emails. Re-read after an env-file reload." introduced:"v0.3.8"`
	FromName          string `env:"FROM_NAME" default:"FrameWorks" desc:"Sender display name for incident emails. Re-read after an env-file reload." introduced:"v0.3.8"`
	SMTPAllowInsecure bool   `env:"SMTP_ALLOW_INSECURE" default:"false" desc:"Sends incident emails to an SMTP server that does not offer STARTTLS. For an isolated development or test relay only; never set it in production. Re-read after an env-file reload." introduced:"v0.3.10"`
}

// HTTPListenPort resolves the HTTP listener port: PORT when set, otherwise
// LOOKOUT_PORT.
func (c *Lookout) HTTPListenPort() string {
	if c.HTTPPortOverride != "" {
		return c.HTTPPortOverride
	}
	return c.HTTPPort
}
