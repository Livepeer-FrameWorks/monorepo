// Package appconfig holds the typed configuration of Bosun, the outbound
// webhook service. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import (
	"errors"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
)

// Bosun is the configuration of the Bosun webhook delivery service. Every
// field applies at startup; a SIGHUP env-file reload updates the process
// environment but does not change a running instance.
//
//configref:service bosun cmd=cmd/bosun
type Bosun struct {
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.EmailBranding

	HTTPPort         string `env:"BOSUN_PORT" default:"@servicedefs.http_port" desc:"TCP port for the HTTP listener that serves health, readiness, and metrics. PORT overrides it." introduced:"v0.3.11"`
	HTTPPortOverride string `env:"PORT" desc:"TCP port for the HTTP listener. Takes precedence over BOSUN_PORT when set." introduced:"v0.3.11"`
	GRPCPort         string `env:"BOSUN_GRPC_PORT" default:"@servicedefs.grpc_port" desc:"TCP port for the gRPC listener that serves the webhook API. Also the port registered with Quartermaster." introduced:"v0.3.11"`
	AdvertiseHost    string `env:"BOSUN_HOST" default:"bosun" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.11"`
	Region           string `env:"REGION" desc:"Region label attached to the audit events Bosun sends to Decklog. Empty lets Decklog stamp its own region." introduced:"v0.3.11"`

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Service token accepted on incoming gRPC calls and sent to Quartermaster, Purser, and Decklog." introduced:"v0.3.11"`
	JWTSecret    string `env:"JWT_SECRET" required:"true" secret:"true" desc:"HMAC secret that verifies the user JWTs Bridge forwards on incoming gRPC calls." introduced:"v0.3.11"`

	FieldEncryptionKey          string `env:"BOSUN_FIELD_ENCRYPTION_KEY" required:"true" secret:"true" desc:"Active master secret, at least 16 bytes, that encrypts webhook signing secrets at rest. Bosun's own key: no other service holds it." introduced:"v0.3.11"`
	FieldEncryptionKeyID        string `env:"BOSUN_FIELD_ENCRYPTION_KEY_ID" default:"primary" desc:"Non-secret label embedded in new signing-secret ciphertext to name the active key." introduced:"v0.3.11"`
	FieldEncryptionPreviousKeys string `env:"BOSUN_FIELD_ENCRYPTION_PREVIOUS_KEYS" secret:"true" desc:"JSON object of retired key IDs to master secrets, kept so signing secrets written with them stay readable after a key rotation." introduced:"v0.3.11"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" required:"true" desc:"Quartermaster gRPC address for service registration." introduced:"v0.3.11"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.11"`
	PurserGRPCAddr                 string `env:"PURSER_GRPC_ADDR" required:"true" desc:"Purser gRPC address for the billing contact that receives the email when an endpoint is disabled automatically." introduced:"v0.3.11"`
	PurserGRPCTLSServerName        string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.11"`
	DecklogGRPCAddr                string `env:"DECKLOG_GRPC_ADDR" required:"true" desc:"Decklog gRPC address that receives the audit events of Bosun's domain event outbox. Events wait in the outbox while Decklog is unreachable." introduced:"v0.3.11"`
	DecklogGRPCTLSServerName       string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.11"`

	KafkaBrokers         []string `env:"KAFKA_BROKERS" required:"true" desc:"Comma-separated bootstrap brokers of the aggregator Kafka cluster. Bosun consumes domain.events and its mirrored copies there." introduced:"v0.3.11"`
	KafkaClusterID       string   `env:"KAFKA_CLUSTER_ID" default:"local" desc:"Kafka cluster identifier attached to the domain event consumer and the dead-letter producer." introduced:"v0.3.11"`
	MirrorRegionPrefixes []string `env:"MIRROR_REGION_PREFIXES" desc:"Comma-separated MirrorMaker2 source cluster aliases whose mirrored domain.events copies are also consumed. Empty consumes only the local topic." introduced:"v0.3.11"`
	DLQTopic             string   `env:"DECKLOG_DLQ_KAFKA_TOPIC" default:"decklog_events_dlq" desc:"Kafka topic that receives domain event records whose payload does not decode." introduced:"v0.3.11"`

	AllowPrivateDestinations bool `env:"BOSUN_ALLOW_PRIVATE_DESTINATIONS" default:"false" desc:"Lets webhook endpoints use private (RFC 1918 / ULA) addresses, plain http, and .local or .internal host names, so an isolated staging or development cluster can deliver to a receiver on its own network. Loopback, link-local, and cloud metadata addresses stay blocked. Never set it on a cluster that serves untrusted tenants." introduced:"v0.3.11"`

	SMTPHost          string `env:"SMTP_HOST" desc:"SMTP server host for the email sent when an endpoint is disabled automatically. Empty leaves those emails queued and retried." introduced:"v0.3.11"`
	SMTPPort          string `env:"SMTP_PORT" default:"587" desc:"SMTP server port." introduced:"v0.3.11"`
	SMTPUser          string `env:"SMTP_USER" desc:"SMTP user." introduced:"v0.3.11"`
	SMTPPassword      string `env:"SMTP_PASSWORD" secret:"true" desc:"Password for SMTP_USER." introduced:"v0.3.11"`
	FromEmail         string `env:"FROM_EMAIL" default:"noreply@frameworks.network" desc:"Sender address for webhook emails." introduced:"v0.3.11"`
	FromName          string `env:"FROM_NAME" default:"FrameWorks" desc:"Sender display name for webhook emails." introduced:"v0.3.11"`
	SMTPAllowInsecure bool   `env:"SMTP_ALLOW_INSECURE" default:"false" desc:"Sends webhook emails to an SMTP server that does not offer STARTTLS. For an isolated development or test relay only; never set it in production." introduced:"v0.3.11"`
}

// Validate checks that the field-encryption settings build a keyring.
func (c *Bosun) Validate() error {
	if _, err := c.FieldKeyring(); err != nil {
		return err
	}
	return nil
}

// FieldKeyringPurpose separates the keys derived for signing secrets from any
// other use of the same master secret.
const FieldKeyringPurpose = "bosun-webhook-signing-secrets"

// FieldKeyring builds the keyring that encrypts webhook signing secrets.
func (c *Bosun) FieldKeyring() (*fieldcrypt.FieldKeyring, error) {
	if strings.TrimSpace(c.FieldEncryptionKey) == "" {
		return nil, errors.New("BOSUN_FIELD_ENCRYPTION_KEY is required")
	}
	previous, err := fieldcrypt.ParseFieldKeySet(c.FieldEncryptionPreviousKeys)
	if err != nil {
		return nil, err
	}
	return fieldcrypt.NewFieldKeyring(c.FieldEncryptionKeyID, []byte(c.FieldEncryptionKey), previous, nil, FieldKeyringPurpose)
}

// HTTPListenPort resolves the HTTP listener port: PORT when set, otherwise
// BOSUN_PORT.
func (c *Bosun) HTTPListenPort() string {
	if c.HTTPPortOverride != "" {
		return c.HTTPPortOverride
	}
	return c.HTTPPort
}
