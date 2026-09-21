package config

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
)

// Configuration blocks shared by service Config structs. A service embeds the
// blocks it uses; scripts/configref expands them into the per-service
// reference.

// HTTPListen is the primary HTTP listener.
type HTTPListen struct {
	Port string `env:"PORT" default:"@servicedefs.http_port" desc:"TCP port for the HTTP listener that serves health, readiness, metrics, and HTTP APIs." introduced:"v0.3.0"`
}

// GRPCListen is the primary gRPC listener.
type GRPCListen struct {
	Port string `env:"GRPC_PORT" default:"@servicedefs.grpc_port" desc:"TCP port for the gRPC listener." introduced:"v0.3.0"`
}

// HTTPRuntime configures the shared Gin router.
type HTTPRuntime struct {
	GinMode        string   `env:"GIN_MODE" default:"debug" desc:"Router mode. release enables Gin release mode and restricts CORS to ALLOWED_ORIGINS; any other value runs the router in development mode." introduced:"v0.3.0"`
	AllowedOrigins []string `env:"ALLOWED_ORIGINS" desc:"Comma-separated browser origins accepted by CORS when GIN_MODE is release." introduced:"v0.3.0"`
}

// Release reports whether the router runs in release mode.
func (r HTTPRuntime) Release() bool {
	return r.GinMode == "release"
}

// ServiceAuth holds the shared internal credentials.
type ServiceAuth struct {
	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service-to-service bearer token for internal gRPC and HTTP calls." introduced:"v0.3.0"`
	JWTSecret    string `env:"JWT_SECRET" required:"true" secret:"true" desc:"HMAC secret that signs and verifies user session JWTs." introduced:"v0.3.0"`
}

// GRPCClientTLS configures TLS for a service's internal gRPC clients. Services
// without a gRPC listener embed it on its own.
type GRPCClientTLS struct {
	CAPath        string `env:"GRPC_TLS_CA_PATH" desc:"CA bundle that internal gRPC clients use to verify servers." introduced:"v0.3.0"`
	AllowInsecure bool   `env:"GRPC_ALLOW_INSECURE" default:"false" desc:"Allows plaintext internal gRPC when no TLS material is configured. Development only." introduced:"v0.3.0"`
}

// GRPCTLS configures TLS for this service's gRPC server and its internal
// gRPC clients.
type GRPCTLS struct {
	GRPCClientTLS
	CertPath string `env:"GRPC_TLS_CERT_PATH" desc:"Certificate presented by this service's gRPC listener." introduced:"v0.3.0"`
	KeyPath  string `env:"GRPC_TLS_KEY_PATH" desc:"Private key for GRPC_TLS_CERT_PATH." introduced:"v0.3.0"`
}

// Postgres is the service's own PostgreSQL or YugabyteDB database.
type Postgres struct {
	DatabaseURL string `env:"DATABASE_URL" required:"true" secret:"true" desc:"Connection URL for this service's PostgreSQL or YugabyteDB database." introduced:"v0.3.0"`
}

// ClickHouse is the analytics store connection.
type ClickHouse struct {
	Addr     []string `env:"CLICKHOUSE_ADDR" required:"true" desc:"Comma-separated ClickHouse native protocol host:port addresses." introduced:"v0.3.0"`
	Database string   `env:"CLICKHOUSE_DB" required:"true" desc:"ClickHouse database that holds the analytics tables." introduced:"v0.3.0"`
	User     string   `env:"CLICKHOUSE_USER" required:"true" desc:"ClickHouse user for this service." introduced:"v0.3.0"`
	Password string   `env:"CLICKHOUSE_PASSWORD" required:"true" secret:"true" desc:"Password for CLICKHOUSE_USER." introduced:"v0.3.0"`
}

// DomainEventActor keys the API token hash a domain event producer records
// for the calling token.
type DomainEventActor struct {
	UsageHashSecret string `env:"USAGE_HASH_SECRET" required:"true" secret:"true" desc:"HMAC key for the user and API token hashes in API usage records. Bridge, Commodore, Purser, and Quartermaster must share one value, so the token hash a domain event records joins Bridge's usage rows for the same token." introduced:"v0.3.11"`
}

// Registration identifies the instance when it registers with Quartermaster.
type Registration struct {
	ClusterID string `env:"CLUSTER_ID" desc:"Cluster the instance registers under in Quartermaster. Empty lets Quartermaster resolve it." introduced:"v0.3.0"`
	NodeID    string `env:"NODE_ID" desc:"Node the instance registers under in Quartermaster." introduced:"v0.3.0"`
}

// BuildEnvironment is the repo-wide runtime selector.
type BuildEnvironment struct {
	BuildEnv string `env:"BUILD_ENV" desc:"Runtime environment selector. Empty, dev, or development enables development-only behavior; production or prod marks a production process." introduced:"v0.3.0"`
}

// IsDevelopment reports whether BUILD_ENV selects development behavior. An
// empty value counts as development.
func (b BuildEnvironment) IsDevelopment() bool {
	switch strings.ToLower(strings.TrimSpace(b.BuildEnv)) {
	case "", "development", "dev":
		return true
	default:
		return false
	}
}

// Logging sets the log level once configuration has loaded. The logger that
// reports configuration errors starts from the level GetLogLevel reads before
// Load runs.
type Logging struct {
	LogLevel string `env:"LOG_LEVEL" default:"info" desc:"Log level: debug, info, warn, or error. Any other value fails startup." introduced:"v0.3.0"`
}

var logLevels = map[string]logrus.Level{
	"debug":   logrus.DebugLevel,
	"info":    logrus.InfoLevel,
	"warn":    logrus.WarnLevel,
	"warning": logrus.WarnLevel,
	"error":   logrus.ErrorLevel,
}

// Validate rejects a LOG_LEVEL outside the supported levels.
func (l Logging) Validate() error {
	if _, ok := logLevels[strings.ToLower(strings.TrimSpace(l.LogLevel))]; !ok {
		return fmt.Errorf("LOG_LEVEL must be debug, info, warn, or error")
	}
	return nil
}

// ApplyLogLevel sets the logger to LOG_LEVEL. A value that did not pass
// Validate leaves the logger unchanged.
func (l Logging) ApplyLogLevel(logger *logrus.Logger) {
	if level, ok := logLevels[strings.ToLower(strings.TrimSpace(l.LogLevel))]; ok && logger != nil {
		logger.SetLevel(level)
	}
}

// TolerantLogging keeps a customer-managed edge running on an unknown log level.
type TolerantLogging struct {
	LogLevel string `env:"LOG_LEVEL" default:"info" desc:"Log level: debug, info, warn, or error. Unknown values keep the current log level." introduced:"v0.3.0"`
}

func (l TolerantLogging) ApplyLogLevel(logger *logrus.Logger) {
	Logging(l).ApplyLogLevel(logger)
}

// Metadata policies accepted by GRPC_METADATA_POLICY. pkg/middleware parses
// the same names into its interceptor policy.
const (
	MetadataPolicyAllow = "allow"
	MetadataPolicyAudit = "audit"
	MetadataPolicyDeny  = "deny"
)

// ValidateMetadataPolicy rejects a GRPC_METADATA_POLICY value outside allow,
// audit, and deny.
func ValidateMetadataPolicy(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case MetadataPolicyAllow, MetadataPolicyAudit, MetadataPolicyDeny:
		return nil
	default:
		return fmt.Errorf("GRPC_METADATA_POLICY must be allow, audit, or deny")
	}
}

// GRPCMetadataPolicy selects how a gRPC server treats caller identity
// metadata on service-token calls.
type GRPCMetadataPolicy struct {
	MetadataPolicy string `env:"GRPC_METADATA_POLICY" default:"allow" desc:"Handling of x-user-id and x-tenant-id metadata on gRPC calls authenticated with the service token: allow applies them to the request, audit applies and logs them, deny ignores them and logs a warning." introduced:"v0.3.0"`
}

// Validate rejects an unknown policy.
func (p GRPCMetadataPolicy) Validate() error {
	return ValidateMetadataPolicy(p.MetadataPolicy)
}

// SystemTenant identifies the deployment's Quartermaster-owned system tenant.
type SystemTenant struct {
	SystemTenantID string `env:"SYSTEM_TENANT_ID" desc:"UUID of the Quartermaster-owned system tenant. Empty uses the reserved ID 00000000-0000-0000-0000-000000000001; a value that is not a UUID fails startup." introduced:"v0.3.0"`
}

// Validate rejects a SYSTEM_TENANT_ID that is not a UUID.
func (s SystemTenant) Validate() error {
	if s.SystemTenantID == "" {
		return nil
	}
	if _, err := uuid.Parse(s.SystemTenantID); err != nil {
		return fmt.Errorf("SYSTEM_TENANT_ID must be a UUID")
	}
	return nil
}

// SystemTenantUUID returns SYSTEM_TENANT_ID, or tenants.SystemTenantID when it
// is empty. A value that did not pass Validate also returns the reserved ID.
func (s SystemTenant) SystemTenantUUID() uuid.UUID {
	if id, err := uuid.Parse(s.SystemTenantID); err == nil {
		return id
	}
	return tenants.SystemTenantID
}

// GeoIP locates the MMDB database used for IP geolocation.
type GeoIP struct {
	MMDBPath string `env:"GEOIP_MMDB_PATH" desc:"Path to an MMDB city database used for IP geolocation. Empty disables GeoIP lookups; a set path that cannot be opened logs a warning and disables lookups." introduced:"v0.3.0"`
}

// DefaultSupportEmail is the support mailbox shown in transactional emails
// when SUPPORT_EMAIL is empty.
const DefaultSupportEmail = "support@frameworks.network"

// EmailBranding is the operator-configured presentation shared by the
// transactional emails a service sends.
type EmailBranding struct {
	LogoURL      string `env:"EMAIL_LOGO_URL" desc:"Absolute URL of the logo image in outgoing emails. Empty uses the light logomark served under WEBAPP_PUBLIC_URL." introduced:"v0.3.10"`
	WebAppURL    string `env:"WEBAPP_PUBLIC_URL" desc:"Public base URL of the web application. Links in outgoing emails and notifications and redirects back to the web application are built from it, and it serves the default email logo when EMAIL_LOGO_URL is empty." introduced:"v0.3.0"`
	SupportEmail string `env:"SUPPORT_EMAIL" desc:"Support mailbox shown in the footer of outgoing emails and set as Reply-To on the emails that carry one. Empty uses support@frameworks.network." introduced:"v0.3.10"`
}

// Logo returns the logo image URL for the email layout: EMAIL_LOGO_URL, or the
// default logomark under WEBAPP_PUBLIC_URL. It returns "" when neither is set.
func (b EmailBranding) Logo() string {
	return email.PublicLogoURL(b.LogoURL, b.WebAppURL)
}

// Support returns SUPPORT_EMAIL, or DefaultSupportEmail when it is empty.
func (b EmailBranding) Support() string {
	if supportEmail := strings.TrimSpace(b.SupportEmail); supportEmail != "" {
		return supportEmail
	}
	return DefaultSupportEmail
}
