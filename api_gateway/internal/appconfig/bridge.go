// Package appconfig holds the typed startup configuration of the Bridge
// gateway. scripts/configref generates the operator configuration reference
// from these structs.
package appconfig

import (
	"errors"
	"strconv"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Bridge is the startup configuration of the Bridge gateway. Fields read
// through the config.Live snapshot at use time follow a SIGHUP env-file
// reload: TrustedProxyCIDRs, BuildEnv, WebappPublicURL, DocsPublicURL, the
// public rate limits, the streaming ports, and PlatformRootDomain. Every other
// field applies at startup.
//
//configref:service bridge cmd=cmd/bridge
type Bridge struct {
	config.HTTPListen
	config.HTTPRuntime
	config.Logging
	config.ServiceAuth
	config.Registration
	config.BuildEnvironment

	AdvertiseHost string `env:"BRIDGE_HOST" default:"bridge" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.0"`
	SourceNode    string `env:"HOSTNAME" default:"bridge" desc:"Node name recorded as the source of API usage events." introduced:"v0.3.0"`
	Region        string `env:"REGION" desc:"Region label attached to API usage events sent to Decklog." introduced:"v0.3.0"`

	CommodoreGRPCAddr     string `env:"COMMODORE_GRPC_ADDR" required:"true" desc:"Commodore gRPC address for authentication, streams, and tenant resources." introduced:"v0.3.0"`
	PeriscopeGRPCAddr     string `env:"PERISCOPE_GRPC_ADDR" required:"true" desc:"Periscope Query gRPC address for analytics queries." introduced:"v0.3.0"`
	PurserGRPCAddr        string `env:"PURSER_GRPC_ADDR" required:"true" desc:"Purser gRPC address for billing, payments, and webhook forwarding." introduced:"v0.3.0"`
	QuartermasterGRPCAddr string `env:"QUARTERMASTER_GRPC_ADDR" required:"true" desc:"Quartermaster gRPC address for tenants, clusters, and service registration." introduced:"v0.3.0"`
	SignalmanGRPCAddr     string `env:"SIGNALMAN_GRPC_ADDR" required:"true" desc:"Local-region Signalman gRPC address for realtime events. GraphQL subscriptions use it when SIGNALMAN_GRPC_ADDRS is empty." introduced:"v0.3.0"`
	DecklogGRPCAddr       string `env:"DECKLOG_GRPC_ADDR" required:"true" desc:"Decklog gRPC address that receives API usage events." introduced:"v0.3.0"`
	NavigatorGRPCAddr     string `env:"NAVIGATOR_GRPC_ADDR" desc:"Navigator gRPC address for tenant alias domains. Empty disables the Navigator client." introduced:"v0.3.0"`
	DeckhandGRPCAddr      string `env:"DECKHAND_GRPC_ADDR" desc:"Deckhand gRPC address for support messaging. Empty disables the Deckhand client." introduced:"v0.3.0"`
	SkipperGRPCAddr       string `env:"SKIPPER_GRPC_ADDR" desc:"Skipper gRPC address for the AI consultant. Empty disables the Skipper gRPC client." introduced:"v0.3.0"`
	LookoutGRPCAddr       string `env:"LOOKOUT_GRPC_ADDR" desc:"Lookout gRPC address for incidents. Empty disables the Lookout client." introduced:"v0.3.8"`
	BosunGRPCAddr         string `env:"BOSUN_GRPC_ADDR" desc:"Bosun gRPC address for outbound webhook endpoints and deliveries. Empty disables the Bosun client, and the webhook API returns an unavailable error." introduced:"v0.3.11"`

	SignalmanGRPCAddrs          string `env:"SIGNALMAN_GRPC_ADDRS" desc:"Comma-separated local-region Signalman replica addresses that GraphQL subscription streams are opened against, tried in a tenant-rotated order. Empty uses SIGNALMAN_GRPC_ADDR." introduced:"v0.3.0"`
	SignalmanConnectTimeoutSecs int    `env:"SIGNALMAN_CONNECT_TIMEOUT_SECONDS" default:"5" desc:"Seconds an upstream Signalman subscription stream waits for a ready connection before trying the next replica. 0 uses 5." introduced:"v0.3.0"`
	WSMaxSubscriptionsPerTenant int    `env:"WS_MAX_SUBSCRIPTIONS_PER_TENANT" default:"100" desc:"Maximum concurrent GraphQL subscriptions per tenant on one Bridge replica. 0 disables the limit." introduced:"v0.3.8"`

	GRPCTLSCAPath     string `env:"GRPC_TLS_CA_PATH" desc:"CA bundle that Bridge uses to verify internal gRPC servers." introduced:"v0.3.0"`
	GRPCAllowInsecure bool   `env:"GRPC_ALLOW_INSECURE" default:"false" desc:"Allows plaintext internal gRPC connections when no TLS material is configured. Development only." introduced:"v0.3.0"`

	CommodoreGRPCTLSServerName     string `env:"COMMODORE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Commodore connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PeriscopeGRPCTLSServerName     string `env:"PERISCOPE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Periscope Query connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PurserGRPCTLSServerName        string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	NavigatorGRPCTLSServerName     string `env:"NAVIGATOR_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Navigator connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	SignalmanGRPCTLSServerName     string `env:"SIGNALMAN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for Signalman connections. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DecklogGRPCTLSServerName       string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DeckhandGRPCTLSServerName      string `env:"DECKHAND_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Deckhand connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	SkipperGRPCTLSServerName       string `env:"SKIPPER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Skipper gRPC connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	LookoutGRPCTLSServerName       string `env:"LOOKOUT_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Lookout connection. Empty uses the canonical internal name." introduced:"v0.3.8"`
	BosunGRPCTLSServerName         string `env:"BOSUN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Bosun connection. Empty uses the canonical internal name." introduced:"v0.3.11"`

	QuartermasterCacheTTLSeconds    int `env:"QUARTERMASTER_CACHE_TTL_SECONDS" default:"60" desc:"Seconds a cached Quartermaster lookup stays fresh." introduced:"v0.3.0"`
	QuartermasterCacheSWRSeconds    int `env:"QUARTERMASTER_CACHE_SWR_SECONDS" default:"30" desc:"Seconds a stale Quartermaster lookup is served while it revalidates." introduced:"v0.3.0"`
	QuartermasterCacheNegTTLSeconds int `env:"QUARTERMASTER_CACHE_NEG_TTL_SECONDS" default:"10" desc:"Seconds a failed Quartermaster lookup is cached." introduced:"v0.3.0"`
	QuartermasterCacheMax           int `env:"QUARTERMASTER_CACHE_MAX" default:"10000" desc:"Maximum entries in the Quartermaster lookup cache." introduced:"v0.3.0"`

	PeriscopeCacheTTLSeconds         int `env:"PERISCOPE_CACHE_TTL_SECONDS" default:"30" desc:"Seconds a cached Periscope analytics query stays fresh." introduced:"v0.3.0"`
	PeriscopeCacheSWRSeconds         int `env:"PERISCOPE_CACHE_SWR_SECONDS" default:"15" desc:"Seconds a stale Periscope analytics result is served while it revalidates." introduced:"v0.3.0"`
	PeriscopeCacheNegTTLSeconds      int `env:"PERISCOPE_CACHE_NEG_TTL_SECONDS" default:"5" desc:"Seconds a failed Periscope analytics query is cached." introduced:"v0.3.0"`
	PeriscopeCacheMax                int `env:"PERISCOPE_CACHE_MAX" default:"5000" desc:"Maximum entries in the Periscope analytics query cache." introduced:"v0.3.0"`
	PeriscopeCacheLoadTimeoutSeconds int `env:"PERISCOPE_CACHE_LOAD_TIMEOUT_SECONDS" default:"30" desc:"Seconds a shared Periscope cache load may run before it is abandoned." introduced:"v0.3.0"`

	CookieDomain     string `env:"COOKIE_DOMAIN" desc:"Domain attribute for auth cookies, leading dot ignored. Empty scopes cookies to the Bridge host; set a parent domain to share them across subdomains." introduced:"v0.3.0"`
	WebappPublicURL  string `env:"WEBAPP_PUBLIC_URL" desc:"Public web application URL returned by /auth/webapp-url for native client browser handoff. Re-read after an env-file reload." introduced:"v0.3.0"`
	GatewayPublicURL string `env:"GATEWAY_PUBLIC_URL" desc:"Public Bridge URL. The MCP schema tools introspect its /graphql/ endpoint; empty uses http://localhost:8080/graphql/." introduced:"v0.3.0"`
	DocsPublicURL    string `env:"DOCS_PUBLIC_URL" desc:"Public documentation URL linked from rate-limit responses. Empty omits the link. Re-read after an env-file reload." introduced:"v0.3.0"`

	PublicRateLimitPerMinute int `env:"PUBLIC_RATE_LIMIT_PER_MINUTE" default:"60" desc:"Requests per minute allowed per client IP for unauthenticated callers. Values below 1 use 60. Re-read after an env-file reload." introduced:"v0.3.0"`
	PublicRateLimitBurst     int `env:"PUBLIC_RATE_LIMIT_BURST" default:"30" desc:"Burst allowance per client IP for unauthenticated callers. Values below 1 use 30. Re-read after an env-file reload." introduced:"v0.3.0"`
	WebhookRateLimitPerMin   int `env:"WEBHOOK_RATE_LIMIT_PER_MIN" default:"300" desc:"Inbound payment-provider webhook requests allowed per minute per client IP. 0 or less disables the webhook rate limit." introduced:"v0.3.0"`

	StreamingSRTPort   int    `env:"STREAMING_SRT_PORT" default:"8889" desc:"SRT ingest port reported in the streamingConfig GraphQL query. Re-read after an env-file reload." introduced:"v0.3.0"`
	StreamingRTMPPort  int    `env:"STREAMING_RTMP_PORT" default:"1935" desc:"RTMP ingest port reported in the streamingConfig GraphQL query. Re-read after an env-file reload." introduced:"v0.3.0"`
	PlatformRootDomain string `env:"BRAND_DOMAIN" desc:"Platform root domain for global streaming host names when cluster routing carries no base URL. Re-read after an env-file reload." introduced:"v0.3.0"`

	TrustedProxyCIDRs string `env:"TRUSTED_PROXY_CIDRS" desc:"Comma-separated CIDRs or IPs of reverse proxies whose forwarding headers identify the client. Re-read after an env-file reload." introduced:"v0.3.0"`
	UsageHashSecret   string `env:"USAGE_HASH_SECRET" secret:"true" desc:"HMAC key for the user and API token hashes in API usage records. Bridge, Commodore, Purser, and Quartermaster must share one value, so the token hash a domain event records joins Bridge's usage rows for the same token. Empty in Bridge uses a random per-process key: hashes change on restart and match no domain event." introduced:"v0.3.0"`

	GraphQLComplexityLimit   int    `env:"GRAPHQL_COMPLEXITY_LIMIT" default:"1000" desc:"Maximum GraphQL operation complexity. 0 disables the limit." introduced:"v0.3.0"`
	GraphQLMaxDepth          int    `env:"GRAPHQL_MAX_DEPTH" default:"10" desc:"Maximum GraphQL selection depth. 0 disables the limit." introduced:"v0.3.0"`
	GraphQLPlaygroundEnabled string `env:"GRAPHQL_PLAYGROUND_ENABLED" desc:"Serves the GraphQL playground at /graphql/playground when true. Empty enables it unless GIN_MODE is release." introduced:"v0.3.0"`

	TelemetryTokenSecret string `env:"TELEMETRY_TOKEN_SECRET" secret:"true" desc:"Shared key that verifies player telemetry tokens. Empty drops serving-cluster attribution from player telemetry." introduced:"v0.3.0"`
	SkipperSpokeURL      string `env:"SKIPPER_SPOKE_URL" default:"http://skipper:18018/mcp/spoke" desc:"Skipper MCP spoke URL that Bridge proxies the ask_consultant tool to." introduced:"v0.3.0"`
	SkillFilesDir        string `env:"SKILL_FILES_DIR" desc:"Directory checked first for SKILL.md, skill.json, and the other agent discovery files." introduced:"v0.3.0"`
	X402GasWalletAddress string `env:"X402_GAS_WALLET_ADDRESS" desc:"Wallet address substituted into did.json. Empty removes the verification method from did.json." introduced:"v0.3.0"`
}

// GatewayGraphQLURL is the GraphQL endpoint under GATEWAY_PUBLIC_URL, or the
// local listener default when it is unset.
func (c *Bridge) GatewayGraphQLURL() string {
	if base := strings.TrimRight(c.GatewayPublicURL, "/"); base != "" {
		return base + "/graphql/"
	}
	return "http://localhost:8080/graphql/"
}

// PlaygroundEnabled resolves GRAPHQL_PLAYGROUND_ENABLED against GIN_MODE.
func (c *Bridge) PlaygroundEnabled() bool {
	if c.GraphQLPlaygroundEnabled == "" {
		return !c.Release()
	}
	enabled, err := strconv.ParseBool(c.GraphQLPlaygroundEnabled)
	return err == nil && enabled
}

// Validate rejects a playground setting that is not a boolean.
func (c *Bridge) Validate() error {
	if c.GraphQLPlaygroundEnabled != "" {
		if _, err := strconv.ParseBool(c.GraphQLPlaygroundEnabled); err != nil {
			return errors.New("GRAPHQL_PLAYGROUND_ENABLED must be true or false")
		}
	}
	return nil
}
