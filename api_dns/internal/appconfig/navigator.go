// Package appconfig holds the typed startup configuration of the Navigator
// binary. scripts/configref generates the operator configuration reference
// from these structs.
package appconfig

import (
	"errors"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Navigator is the configuration of the Navigator service. The ACME issuance
// settings (ACME_ENV, NAVIGATOR_ACME_CA_ORDER, NAVIGATOR_GOOGLE_TRUST_*,
// NAVIGATOR_CERT_ALLOWED_SUFFIXES, and BRAND_DOMAIN for the allowlist
// fallback) are read through a config.Live snapshot on every certificate
// order, so they follow a SIGHUP env-file reload; every other field applies
// at startup.
//
//configref:service navigator cmd=cmd/navigator
type Navigator struct {
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.BuildEnvironment

	HTTPPortOverride string `env:"PORT" desc:"TCP port for the HTTP listener. Takes precedence over NAVIGATOR_PORT when set." introduced:"v0.3.0"`
	HTTPPort         string `env:"NAVIGATOR_PORT" required:"true" desc:"TCP port for the HTTP listener that serves health, readiness, metrics, and the internal TLS bundle endpoint. PORT overrides it." introduced:"v0.3.0"`
	GRPCPort         string `env:"NAVIGATOR_GRPC_PORT" required:"true" desc:"TCP port for the Navigator gRPC listener. Also the port registered with Quartermaster." introduced:"v0.3.0"`
	HTTPTLSCertFile  string `env:"NAVIGATOR_HTTP_TLS_CERT_FILE" desc:"Certificate for serving the HTTP listener over TLS. Set together with NAVIGATOR_HTTP_TLS_KEY_FILE." introduced:"v0.3.0"`
	HTTPTLSKeyFile   string `env:"NAVIGATOR_HTTP_TLS_KEY_FILE" desc:"Private key for NAVIGATOR_HTTP_TLS_CERT_FILE." introduced:"v0.3.0"`
	AdvertiseHost    string `env:"NAVIGATOR_HOST" default:"navigator" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`

	ServiceToken       string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service-to-service bearer token. Authenticates gRPC callers, the internal TLS bundle endpoint, and Navigator calls to Quartermaster." introduced:"v0.3.0"`
	FieldEncryptionKey string `env:"FIELD_ENCRYPTION_KEY" required:"true" secret:"true" desc:"Key material from which Navigator derives the encryption key for stored certificate and CA private keys." introduced:"v0.3.0"`

	QuartermasterGRPCAddr          string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address for cluster, node, and bootstrap token lookups and self-registration." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`

	DecklogGRPCAddr          string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address that receives custom domain domain events from Navigator's outbox. Events wait in the outbox while Decklog is unreachable." introduced:"v0.3.11"`
	DecklogGRPCTLSServerName string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.11"`
	Region                   string `env:"REGION" desc:"Region label attached to domain events sent to Decklog. Empty lets Decklog stamp its own region." introduced:"v0.3.11"`

	RootDomain string `env:"BRAND_DOMAIN" required:"true" desc:"Platform root domain whose DNS records and certificates Navigator manages. Also the certificate allowlist when NAVIGATOR_CERT_ALLOWED_SUFFIXES is empty; that use follows an env-file reload." introduced:"v0.3.0"`
	ACMEEmail  string `env:"ACME_EMAIL" required:"true" desc:"Contact email for the ACME accounts that issue platform certificates." introduced:"v0.3.0"`

	ACMEEnv                 string `env:"ACME_ENV" desc:"ACME environment. staging uses the staging directories of Let's Encrypt and Google Trust Services; any other value uses production. Re-read after an env-file reload." introduced:"v0.3.0"`
	ACMECAOrder             string `env:"NAVIGATOR_ACME_CA_ORDER" desc:"Comma-separated CA preference order for new certificates (letsencrypt, google-trust). Empty uses Let's Encrypt, then Google Trust Services when EAB credentials are set. Re-read after an env-file reload." introduced:"v0.3.0"`
	GoogleTrustDirectoryURL string `env:"NAVIGATOR_GOOGLE_TRUST_DIRECTORY_URL" desc:"ACME directory URL for Google Trust Services. Empty uses the public directory for the ACME_ENV environment. Re-read after an env-file reload." introduced:"v0.3.0"`
	GoogleTrustEABKeyID     string `env:"NAVIGATOR_GOOGLE_TRUST_EAB_KID" desc:"External Account Binding key ID for Google Trust Services. Set together with NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY. Re-read after an env-file reload." introduced:"v0.3.0"`
	GoogleTrustEABHMACKey   string `env:"NAVIGATOR_GOOGLE_TRUST_EAB_HMAC_KEY" secret:"true" desc:"External Account Binding HMAC key for Google Trust Services. Re-read after an env-file reload." introduced:"v0.3.0"`
	CertAllowedSuffixes     string `env:"NAVIGATOR_CERT_ALLOWED_SUFFIXES" desc:"Comma-separated domain suffixes Navigator may issue certificates for. Empty falls back to BRAND_DOMAIN. Re-read after an env-file reload." introduced:"v0.3.0"`

	ProxyServices string `env:"NAVIGATOR_PROXY_SERVICES" desc:"Comma-separated service types whose Cloudflare records are proxied. Empty uses the built-in list of web surfaces; livepeer-gateway is never proxied." introduced:"v0.3.0"`

	CloudflareAPIToken    string `env:"CLOUDFLARE_API_TOKEN" required:"true" secret:"true" desc:"Cloudflare API token for DNS records and load balancers. Also used for ACME DNS-01 challenges when CLOUDFLARE_DNS_API_TOKEN is empty." introduced:"v0.3.0"`
	CloudflareZoneID      string `env:"CLOUDFLARE_ZONE_ID" required:"true" desc:"Cloudflare zone ID of the platform root domain." introduced:"v0.3.0"`
	CloudflareAccountID   string `env:"CLOUDFLARE_ACCOUNT_ID" required:"true" desc:"Cloudflare account ID that owns the load balancer pools and monitors." introduced:"v0.3.0"`
	CloudflareDNSAPIToken string `env:"CLOUDFLARE_DNS_API_TOKEN" secret:"true" desc:"Cloudflare token the ACME DNS-01 provider uses. Empty is filled from CLOUDFLARE_API_TOKEN at startup." introduced:"v0.3.0"`

	BunnyAPIKey     string `env:"BUNNY_API_KEY" secret:"true" desc:"Bunny API key for media cluster and tenant alias DNS zones. Empty makes media cluster DNS fall back to Cloudflare." introduced:"v0.3.0"`
	BunnyAPIBaseURL string `env:"BUNNY_API_BASE_URL" desc:"Bunny API base URL. Empty uses the public Bunny API." introduced:"v0.3.0"`

	DNSRecordTTLSeconds            int  `env:"NAVIGATOR_DNS_TTL_A_RECORD" default:"60" desc:"TTL in seconds for the DNS records Navigator publishes outside Cloudflare load balancers." introduced:"v0.3.0"`
	DNSLoadBalancerTTLSeconds      int  `env:"NAVIGATOR_DNS_TTL_LB" default:"60" desc:"TTL in seconds for Cloudflare load balancer records." introduced:"v0.3.0"`
	DNSHealthStaleSeconds          int  `env:"NAVIGATOR_DNS_HEALTH_STALE_SECONDS" default:"300" desc:"Age in seconds after which a node health report is too old to publish in DNS. Quartermaster reads the same key so both apply one freshness gate." introduced:"v0.3.0"`
	LoadBalancerMonitorInterval    int  `env:"NAVIGATOR_CF_MONITOR_INTERVAL" default:"60" desc:"Interval in seconds between Cloudflare load balancer monitor probes." introduced:"v0.3.0"`
	LoadBalancerMonitorTimeout     int  `env:"NAVIGATOR_CF_MONITOR_TIMEOUT" default:"5" desc:"Timeout in seconds for one Cloudflare load balancer monitor probe." introduced:"v0.3.0"`
	LoadBalancerMonitorRetries     int  `env:"NAVIGATOR_CF_MONITOR_RETRIES" default:"2" desc:"Retries before a Cloudflare load balancer monitor marks an origin unhealthy." introduced:"v0.3.0"`
	DNSReconcileIntervalSeconds    int  `env:"NAVIGATOR_DNS_RECONCILE_INTERVAL_SECONDS" default:"60" desc:"Interval in seconds between full DNS and certificate reconcile passes." introduced:"v0.3.0"`
	DNSRecordsEnabled              bool `env:"NAVIGATOR_DNS_RECORDS_ENABLED" default:"true" desc:"Publish public DNS records. Disable for isolated staging while retaining certificate management." introduced:"v0.3.11"`
	AliasApplyStateIntervalSeconds int  `env:"NAVIGATOR_ALIAS_APPLY_STATE_INTERVAL_SECONDS" default:"15" desc:"Interval in seconds between tenant alias DNS passes over the per-edge apply state." introduced:"v0.3.0"`

	InternalCARootCertFile           string `env:"NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE" desc:"Root certificate file imported as the internal CA when none is stored. Set together with the intermediate certificate and key files." introduced:"v0.3.0"`
	InternalCAIntermediateCertFile   string `env:"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_FILE" desc:"Intermediate certificate file imported with NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE." introduced:"v0.3.0"`
	InternalCAIntermediateKeyFile    string `env:"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_FILE" desc:"Intermediate private key file imported with NAVIGATOR_INTERNAL_CA_ROOT_CERT_FILE." introduced:"v0.3.0"`
	InternalCARootCertPEMB64         string `env:"NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64" desc:"Base64-wrapped root certificate PEM imported as the internal CA when none is stored. Takes precedence over the file settings; set together with the intermediate certificate and key." introduced:"v0.3.0"`
	InternalCAIntermediateCertPEMB64 string `env:"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_CERT_PEM_B64" desc:"Base64-wrapped intermediate certificate PEM imported with NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64." introduced:"v0.3.0"`
	InternalCAIntermediateKeyPEMB64  string `env:"NAVIGATOR_INTERNAL_CA_INTERMEDIATE_KEY_PEM_B64" secret:"true" desc:"Base64-wrapped intermediate private key PEM imported with NAVIGATOR_INTERNAL_CA_ROOT_CERT_PEM_B64." introduced:"v0.3.0"`
}

// Validate enforces the paired HTTP TLS file settings.
func (c *Navigator) Validate() error {
	if (c.HTTPTLSCertFile == "") != (c.HTTPTLSKeyFile == "") {
		return errors.New("NAVIGATOR_HTTP_TLS_CERT_FILE and NAVIGATOR_HTTP_TLS_KEY_FILE must be set together")
	}
	return nil
}

// ListenHTTPPort returns the HTTP listener port: PORT when set, otherwise
// NAVIGATOR_PORT.
func (c *Navigator) ListenHTTPPort() string {
	if c.HTTPPortOverride != "" {
		return c.HTTPPortOverride
	}
	return c.HTTPPort
}

// IsProduction reports whether BUILD_ENV marks a production process, which
// requires imported internal CA material.
func (c *Navigator) IsProduction() bool {
	switch strings.ToLower(strings.TrimSpace(c.BuildEnv)) {
	case "production", "prod":
		return true
	default:
		return false
	}
}
