// Package appconfig holds the typed startup configuration of the Commodore
// binary and its subcommands. scripts/configref generates the operator
// configuration reference from these structs.
package appconfig

import (
	"errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// FieldEncryptionRotation is the non-active field-encryption key material read
// by the server and the bootstrap subcommand.
type FieldEncryptionRotation struct {
	FieldEncryptionKeyID         string `env:"FIELD_ENCRYPTION_KEY_ID" default:"primary" desc:"Non-secret label embedded in new ciphertext envelopes to name the active field-encryption key." introduced:"v0.3.0"`
	FieldEncryptionPreviousKeys  string `env:"FIELD_ENCRYPTION_PREVIOUS_KEYS" secret:"true" desc:"JSON object of retired key IDs to master secrets, kept so existing ciphertext stays readable." introduced:"v0.3.0"`
	FieldEncryptionLegacySecrets string `env:"FIELD_ENCRYPTION_LEGACY_SECRETS" secret:"true" desc:"Read-only v1 and v2 master secrets for decrypting historical ciphertext. Never used for new writes." introduced:"v0.3.0"`
}

// QuartermasterClient is the Quartermaster connection read by the server and
// the bootstrap subcommand.
type QuartermasterClient struct {
	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address for tenant, cluster, and registration calls." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
}

// Commodore is the startup configuration of the Commodore server.
//
//configref:service commodore cmd=cmd/commodore
type Commodore struct {
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.ServiceAuth
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.BuildEnvironment
	config.SystemTenant
	config.EmailBranding
	config.DomainEventActor
	FieldEncryptionRotation
	QuartermasterClient

	Region        string `env:"REGION" desc:"Region label attached to service events sent to Decklog." introduced:"v0.3.0"`
	AdvertiseHost string `env:"COMMODORE_HOST" default:"commodore" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`

	FieldEncryptionKey string `env:"FIELD_ENCRYPTION_KEY" required:"true" secret:"true" desc:"Active master secret for application-field encryption. New ciphertext is written with it." introduced:"v0.3.0"`

	RestreamAllowPrivateDestinations string `env:"RESTREAM_ALLOW_PRIVATE_DESTINATIONS" desc:"Allows multistream destinations that resolve to private addresses when set to true or 1." introduced:"v0.3.0"`
	RestreamAllowedPrivateCIDRs      string `env:"RESTREAM_ALLOWED_PRIVATE_CIDRS" desc:"Comma-separated CIDRs exempted from the private, loopback, and link-local multistream destination block." introduced:"v0.3.0"`
	RestreamDeniedCIDRs              string `env:"RESTREAM_DENIED_CIDRS" desc:"Comma-separated CIDRs never allowed as multistream destinations. Denials win over allowances." introduced:"v0.3.0"`

	PlaybackWebhookAllowPrivateDestinations bool `env:"PLAYBACK_WEBHOOK_ALLOW_PRIVATE_DESTINATIONS" default:"false" desc:"Lets playback-auth webhook URLs use private (RFC 1918 / ULA) addresses, plain http, and .local or .internal host names, so an isolated staging or development cluster can authorize playback against a receiver on its own network. Loopback, link-local, and cloud metadata addresses stay blocked. Must match Foghorn's setting. Never set it on a cluster that serves untrusted tenants." introduced:"v0.3.11"`

	MediaAuthoritySigningKeyID            string `env:"MEDIA_AUTHORITY_SIGNING_KEY_ID" desc:"Key ID of the Ed25519 signer for media authority envelopes. Required outside development, together with MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64." introduced:"v0.3.0"`
	MediaAuthoritySigningPrivateKeyPEMB64 string `env:"MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64" secret:"true" desc:"Base64-wrapped PKCS#8 Ed25519 private key PEM that signs media authority envelopes." introduced:"v0.3.0"`
	MediaAuthoritySealRecipients          string `env:"MEDIA_AUTHORITY_SEAL_RECIPIENTS" desc:"Rendered by the CLI. Maps control-cell IDs to X25519 recipient keys and enables sealed-secret delivery in media authority." introduced:"v0.3.0"`

	NavigatorGRPCAddr string `env:"NAVIGATOR_GRPC_ADDR" desc:"Navigator gRPC address for tenant alias status. Empty disables tenant alias lookups." introduced:"v0.3.0"`
	PurserGRPCAddr    string `env:"PURSER_GRPC_ADDR" default:"purser:19003" desc:"Purser gRPC address for billing and user-limit checks." introduced:"v0.3.0"`
	DecklogGRPCAddr   string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address for service events." introduced:"v0.3.0"`

	FoghornGRPCTLSServerName   string `env:"FOGHORN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for Foghorn connections. Empty uses the canonical internal name." introduced:"v0.3.0"`
	NavigatorGRPCTLSServerName string `env:"NAVIGATOR_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Navigator connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PurserGRPCTLSServerName    string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DecklogGRPCTLSServerName   string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.0"`

	ListmonkURL          string `env:"LISTMONK_URL" desc:"Listmonk base URL. Together with LISTMONK_API_USERNAME and LISTMONK_API_TOKEN it enables newsletter subscription." introduced:"v0.3.0"`
	ListmonkAPIUsername  string `env:"LISTMONK_API_USERNAME" desc:"Listmonk API user for newsletter subscription." introduced:"v0.3.0"`
	ListmonkAPIToken     string `env:"LISTMONK_API_TOKEN" secret:"true" desc:"Listmonk API token for LISTMONK_API_USERNAME." introduced:"v0.3.0"`
	DefaultMailingListID int    `env:"DEFAULT_MAILING_LIST_ID" default:"1" desc:"Listmonk list ID that newsletter subscriptions are added to." introduced:"v0.3.0"`

	TurnstileAuthSecretKey string `env:"TURNSTILE_AUTH_SECRET_KEY" secret:"true" desc:"Cloudflare Turnstile secret key for authentication bot checks. Empty disables Turnstile verification." introduced:"v0.3.0"`
	TurnstileFailOpen      bool   `env:"TURNSTILE_FAIL_OPEN" default:"false" desc:"Lets a request proceed when the Turnstile verification call itself fails. A rejected challenge is always refused." introduced:"v0.3.0"`
	PasswordResetSecret    string `env:"PASSWORD_RESET_SECRET" secret:"true" desc:"HMAC key for hashing stored account tokens such as password reset tokens. Empty falls back to plain SHA-256." introduced:"v0.3.0"`

	ChandlerBaseURL string `env:"CHANDLER_BASE_URL" desc:"Public Chandler origin used for every thumbnail asset URL instead of the per-cluster Chandler origins learned from Quartermaster." introduced:"v0.3.0"`

	DeviceVerificationURL string `env:"DEVICE_VERIFICATION_URL" desc:"URL users visit to approve a device authorization code. Empty uses WEBAPP_PUBLIC_URL with /device appended. Re-read after an env-file reload." introduced:"v0.3.0"`
	PlatformRootDomain    string `env:"PLATFORM_ROOT_DOMAIN" desc:"Root domain for the global ingest, edge, play, Chandler, and Livepeer host names returned on stream creation and for Mist admin edge domains. Empty falls back to BRAND_DOMAIN. Re-read after an env-file reload." introduced:"v0.3.0"`
	BrandDomain           string `env:"BRAND_DOMAIN" desc:"Root domain used when PLATFORM_ROOT_DOMAIN is empty. With both empty, global stream host names are omitted and Mist admin edge domains use frameworks.network. Re-read after an env-file reload." introduced:"v0.3.0"`

	SMTPHost     string `env:"SMTP_HOST" desc:"SMTP server for email verification and password reset messages. Empty skips sending them. Re-read after an env-file reload." introduced:"v0.3.0"`
	SMTPPort     string `env:"SMTP_PORT" default:"587" desc:"SMTP server port. Re-read after an env-file reload." introduced:"v0.3.0"`
	SMTPUser     string `env:"SMTP_USER" desc:"SMTP authentication user. Re-read after an env-file reload." introduced:"v0.3.0"`
	SMTPPassword string `env:"SMTP_PASSWORD" secret:"true" desc:"SMTP authentication password for SMTP_USER. Re-read after an env-file reload." introduced:"v0.3.0"`
	FromEmail    string `env:"FROM_EMAIL" default:"noreply@frameworks.network" desc:"Sender address for email verification and password reset messages. Re-read after an env-file reload." introduced:"v0.3.0"`
	FromName     string `env:"FROM_NAME" desc:"Sender display name for email verification and password reset messages. Empty uses FrameWorks. Re-read after an env-file reload." introduced:"v0.3.0"`

	SMTPAllowInsecure bool `env:"SMTP_ALLOW_INSECURE" default:"false" desc:"Sends email verification and password reset messages to an SMTP server that does not offer STARTTLS. For an isolated development or test relay only; never set it in production. Re-read after an env-file reload." introduced:"v0.3.10"`
}

// Validate enforces the media authority signer pairing and its production
// requirement.
func (c *Commodore) Validate() error {
	hasKeyID := c.MediaAuthoritySigningKeyID != ""
	hasKey := c.MediaAuthoritySigningPrivateKeyPEMB64 != ""
	if hasKeyID != hasKey {
		return errors.New("MEDIA_AUTHORITY_SIGNING_KEY_ID and MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64 must be configured together")
	}
	if !hasKey && !c.IsDevelopment() {
		return errors.New("MEDIA_AUTHORITY_SIGNING_KEY_ID and MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64 are required outside development")
	}
	return nil
}

// CommodoreDataMigrations is the configuration of `commodore data-migrations`.
// The field-encryption secrets are optional at load time because listing and
// status commands never touch ciphertext; the field-encryption migration
// checks them when it runs or verifies.
//
//configref:service commodore cmd=cmd/commodore variant=data-migrations
type CommodoreDataMigrations struct {
	config.Postgres
	FieldEncryptionRotation

	FieldEncryptionKey string `env:"FIELD_ENCRYPTION_KEY" secret:"true" desc:"Active field-encryption master secret that the field-encryption migration re-encrypts rows with. Required when that migration runs or verifies." introduced:"v0.3.0"`
	JWTSecret          string `env:"JWT_SECRET" secret:"true" desc:"Legacy field-encryption key for v1 and v2 ciphertext. Required by the field-encryption migration, which refuses to continue if it changes mid-run." introduced:"v0.3.0"`

	FieldEncryptionAllowQuarantine        string `env:"FIELD_ENCRYPTION_ALLOW_QUARANTINE" desc:"Set to true or 1 to let field-encryption verification pass while undecryptable rows remain quarantined." introduced:"v0.3.0"`
	FieldEncryptionRequeueQuarantine      string `env:"FIELD_ENCRYPTION_REQUEUE_QUARANTINE" desc:"Operator-chosen token. A value different from the last one checkpointed restarts the field-encryption sweep and retries previously quarantined rows. Empty, 0, or false disables retries." introduced:"v0.3.0"`
	FieldEncryptionAckUnverifiedLegacyKey string `env:"FIELD_ENCRYPTION_ACK_UNVERIFIED_LEGACY_KEY" desc:"Set to I_ACCEPT_UNVERIFIED_LEGACY_KEY to start the field-encryption migration when no sampled v1 or v2 row decrypts with JWT_SECRET or the previous keys." introduced:"v0.3.0"`
}

// CommodoreBootstrap is the configuration of `commodore bootstrap`. The
// encryption secrets are optional here because only a desired state that
// declares pull streams encrypts source URIs; the subcommand checks them when
// it needs them. The Purser and Foghorn connections are dialed only when a
// declared source location names nodes.
//
//configref:service commodore cmd=cmd/commodore variant=bootstrap
type CommodoreBootstrap struct {
	config.Postgres
	QuartermasterClient
	FieldEncryptionRotation
	config.GRPCClientTLS
	config.SystemTenant

	ServiceToken       string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service token for resolving tenant aliases through Quartermaster." introduced:"v0.3.0"`
	JWTSecret          string `env:"JWT_SECRET" secret:"true" desc:"Included as legacy field-encryption key material for pull-stream source URIs. Required when the desired state declares pull streams." introduced:"v0.3.0"`
	FieldEncryptionKey string `env:"FIELD_ENCRYPTION_KEY" secret:"true" desc:"Active field-encryption master secret. Required when the desired state declares pull streams." introduced:"v0.3.0"`

	PurserGRPCAddr           string `env:"PURSER_GRPC_ADDR" default:"purser:19003" desc:"Purser gRPC address for the node placement check that runs when a declared source location names nodes." introduced:"v0.3.8"`
	PurserGRPCTLSServerName  string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.8"`
	FoghornGRPCTLSServerName string `env:"FOGHORN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Foghorn connections of the node placement check. Empty uses the canonical internal name." introduced:"v0.3.8"`
}
