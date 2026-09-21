// Package appconfig holds the typed startup configuration of the Deckhand
// support messaging service. scripts/configref generates the operator
// configuration reference from these structs.
package appconfig

import (
	"net"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Deckhand is the startup configuration of the Deckhand support messaging
// service. Every field applies at startup; configuration changes require a restart.
//
//configref:service deckhand cmd=cmd/deckhand
type Deckhand struct {
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GRPCTLS
	config.Registration

	HTTPPort   string `env:"DECKHAND_PORT" default:"@servicedefs.http_port" desc:"TCP port for the HTTP listener that serves the Chatwoot webhook, health, readiness, and metrics. Also the port registered with Quartermaster." introduced:"v0.3.0"`
	LegacyPort string `env:"PORT" deprecated:"v0.3.0" replacement:"DECKHAND_PORT" desc:"Overrides DECKHAND_PORT for the HTTP listener and the registered port when set." introduced:"v0.3.0"`
	GRPCPort   string `env:"DECKHAND_GRPC_PORT" default:"@servicedefs.grpc_port" desc:"TCP port for the gRPC listener that serves the support messaging API." introduced:"v0.3.0"`

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Service token accepted on incoming gRPC calls and sent to Quartermaster, Purser, and Decklog." introduced:"v0.3.0"`
	JWTSecret    string `env:"JWT_SECRET" secret:"true" desc:"HMAC secret that verifies user JWTs on incoming gRPC calls. Empty accepts only the service token." introduced:"v0.3.0"`

	ChatwootAPIToken  string `env:"CHATWOOT_API_TOKEN" required:"true" secret:"true" desc:"Chatwoot API access token used for conversation and message calls." introduced:"v0.3.0"`
	ChatwootHost      string `env:"CHATWOOT_HOST" default:"chatwoot" desc:"Chatwoot host. Deckhand calls http://CHATWOOT_HOST:CHATWOOT_PORT." introduced:"v0.3.0"`
	ChatwootPort      string `env:"CHATWOOT_PORT" default:"3000" desc:"Chatwoot HTTP port paired with CHATWOOT_HOST." introduced:"v0.3.0"`
	ChatwootAccountID int    `env:"CHATWOOT_ACCOUNT_ID" default:"1" desc:"Chatwoot account that holds support conversations." introduced:"v0.3.0"`
	ChatwootInboxID   int    `env:"CHATWOOT_INBOX_ID" default:"1" desc:"Chatwoot inbox in which new support conversations are created." introduced:"v0.3.0"`

	WebhookRateLimitPerMin int    `env:"DECKHAND_WEBHOOK_RATE_LIMIT_PER_MIN" default:"600" desc:"Chatwoot webhook requests allowed per client per minute. 0 or less disables the limit." introduced:"v0.3.0"`
	RedisAddr              string `env:"REDIS_ADDR" desc:"Redis address for Chatwoot webhook deduplication. Empty or unreachable disables deduplication." introduced:"v0.3.0"`
	Region                 string `env:"REGION" desc:"Region label attached to service events sent to Decklog." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address for tenant lookups, service events, and registration." introduced:"v0.3.0"`
	PurserGRPCAddr                 string `env:"PURSER_GRPC_ADDR" default:"purser:19003" desc:"Purser gRPC address for billing context lookups." introduced:"v0.3.0"`
	DecklogGRPCAddr                string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address for realtime service events." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PurserGRPCTLSServerName        string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DecklogGRPCTLSServerName       string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	AdvertiseHost                  string `env:"DECKHAND_HOST" default:"deckhand" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
}

// HTTPListenPort resolves the HTTP listener port: PORT when set, otherwise DECKHAND_PORT.
func (c *Deckhand) HTTPListenPort() string {
	if c.LegacyPort != "" {
		return c.LegacyPort
	}
	return c.HTTPPort
}

// ChatwootBaseURL is the Chatwoot API base URL.
func (c *Deckhand) ChatwootBaseURL() string {
	return "http://" + net.JoinHostPort(c.ChatwootHost, c.ChatwootPort)
}
