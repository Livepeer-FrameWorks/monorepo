// Package appconfig holds the typed startup configuration of the Foghorn
// binary and its data-migrations subcommand. scripts/configref generates the
// operator configuration reference from these structs.
package appconfig

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// FoghornRedis is the Redis connection that Foghorn replicas share state,
// relay grants, leases, and the stream registry through.
type FoghornRedis struct {
	RedisURL              string   `env:"REDIS_URL" secret:"true" desc:"Redis URL for state shared between Foghorn replicas. Used when REDIS_MODE is empty; empty with no REDIS_MODE runs without shared state." introduced:"v0.3.0"`
	RedisMode             string   `env:"REDIS_MODE" desc:"Redis topology for the shared state client: single, sentinel, or cluster. Empty uses REDIS_URL. sentinel marks this instance as an HA replica: startup waits for the topology and exits if Redis or the HA command relay cannot be enabled, and a relay that is not ready reports unhealthy." introduced:"v0.3.0"`
	RedisAddrs            []string `env:"REDIS_ADDRS" desc:"Comma-separated Redis, Sentinel, or cluster host:port addresses used when REDIS_MODE is set." introduced:"v0.3.0"`
	RedisMasterName       string   `env:"REDIS_MASTER_NAME" desc:"Sentinel master name. Required when REDIS_MODE is sentinel." introduced:"v0.3.0"`
	RedisUsername         string   `env:"REDIS_USERNAME" desc:"Username for the Redis data connection when REDIS_MODE is set." introduced:"v0.3.0"`
	RedisPassword         string   `env:"REDIS_PASSWORD" secret:"true" desc:"Password for the Redis data connection when REDIS_MODE is set." introduced:"v0.3.0"`
	RedisSentinelUsername string   `env:"REDIS_SENTINEL_USERNAME" desc:"Username for the Sentinel connection when REDIS_MODE is sentinel." introduced:"v0.3.0"`
	RedisSentinelPassword string   `env:"REDIS_SENTINEL_PASSWORD" secret:"true" desc:"Password for the Sentinel connection when REDIS_MODE is sentinel." introduced:"v0.3.0"`
}

// FoghornListeners are the HTTP and gRPC listeners beyond the public PORT.
type FoghornListeners struct {
	PublicHTTPBindAddr   string `env:"FOGHORN_PUBLIC_HTTP_BIND_ADDR" desc:"Bind address of the public HTTP listener on PORT that serves playback, ingest, and Livepeer webhook routes. Empty listens on all interfaces." introduced:"v0.3.0"`
	InternalHTTPPort     string `env:"FOGHORN_INTERNAL_HTTP_PORT" default:"18027" desc:"TCP port of the internal HTTP listener that serves node administration, the dashboard, debug routes, and authenticated Mist source lookups." introduced:"v0.3.0"`
	InternalHTTPBindAddr string `env:"FOGHORN_INTERNAL_HTTP_BIND_ADDR" default:"127.0.0.1" desc:"Bind address of the internal HTTP listener." introduced:"v0.3.0"`
	InternalGRPCBindAddr string `env:"FOGHORN_INTERNAL_GRPC_BIND_ADDR" default:":18019" desc:"host:port of the internal gRPC listener for mesh services, federation, and the HA command relay. It serves GRPC_TLS_CERT_PATH." introduced:"v0.3.0"`
	ExternalGRPCBindAddr string `env:"FOGHORN_EXTERNAL_GRPC_BIND_ADDR" default:":18029" desc:"host:port of the external gRPC listener for Helmsman control streams and edge provisioning. It serves Navigator-issued cluster certificates." introduced:"v0.3.0"`
	InternalGRPCPort     int    `env:"FOGHORN_INTERNAL_GRPC_PORT" desc:"gRPC port this instance registers with Quartermaster. 0 or unset uses the port of FOGHORN_INTERNAL_GRPC_BIND_ADDR." introduced:"v0.3.0"`

	AdvertiseHost      string `env:"FOGHORN_HOST" desc:"Host name registered with Quartermaster, used for the HA relay address when FOGHORN_RELAY_ADVERTISE_HOST is empty, and a fallback host for Mist balancer URLs." introduced:"v0.3.0"`
	RelayAdvertiseAddr string `env:"FOGHORN_RELAY_ADVERTISE_ADDR" desc:"host:port other replicas use to relay commands to this one. Overrides FOGHORN_RELAY_ADVERTISE_HOST and the port derived from FOGHORN_INTERNAL_GRPC_BIND_ADDR." introduced:"v0.3.0"`
	RelayAdvertiseHost string `env:"FOGHORN_RELAY_ADVERTISE_HOST" desc:"Host other replicas use to relay commands to this one, combined with the internal gRPC port. Production refuses a loopback fallback when no host is known." introduced:"v0.3.0"`
	PublicBaseURL      string `env:"FOGHORN_PUBLIC_BASE" desc:"HTTP base URL Mist uses as its balancer source. Overrides the cluster-scoped Foghorn DNS name." introduced:"v0.3.0"`
	FoghornURL         string `env:"FOGHORN_URL" desc:"Foghorn base URL for Mist balancer sources when no cluster DNS name applies. Preferred over the cluster DNS name when BUILD_ENV is dev, development, local, or test." introduced:"v0.3.0"`
}

// FoghornClients are the control-plane gRPC connections.
type FoghornClients struct {
	DecklogGRPCAddr       string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address for trigger, routing, artifact, and federation events." introduced:"v0.3.0"`
	QuartermasterGRPCAddr string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"Quartermaster gRPC address for registration, cluster descriptors, served cluster assignments, and edge health." introduced:"v0.3.0"`
	CommodoreGRPCAddr     string `env:"COMMODORE_GRPC_ADDR" default:"commodore:19001" desc:"Commodore gRPC address for stream resolution and media authority replay." introduced:"v0.3.0"`
	PurserGRPCAddr        string `env:"PURSER_GRPC_ADDR" default:"purser:19003" desc:"Purser gRPC address for x402 settlement and billing checks." introduced:"v0.3.0"`
	NavigatorGRPCAddr     string `env:"NAVIGATOR_GRPC_ADDR" desc:"Navigator gRPC address for cluster TLS bundles and ConfigSeed apply acknowledgements. Empty disables TLS bundle seeding." introduced:"v0.3.0"`

	DecklogGRPCTLSServerName       string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	QuartermasterGRPCTLSServerName string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	CommodoreGRPCTLSServerName     string `env:"COMMODORE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Commodore connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PurserGRPCTLSServerName        string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	NavigatorGRPCTLSServerName     string `env:"NAVIGATOR_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Navigator connection. Empty uses the canonical internal name." introduced:"v0.3.0"`
	FoghornGRPCTLSServerName       string `env:"FOGHORN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for connections to other Foghorn instances for federation and the HA command relay. Empty uses the canonical internal name." introduced:"v0.3.0"`
}

// FoghornCaches tunes the in-process caches.
type FoghornCaches struct {
	CommodoreCacheTTL         time.Duration `env:"COMMODORE_CACHE_TTL" default:"60s" desc:"Freshness window of cached Commodore responses." introduced:"v0.3.0"`
	CommodoreCacheSWR         time.Duration `env:"COMMODORE_CACHE_SWR" default:"30s" desc:"Stale-while-revalidate window of cached Commodore responses." introduced:"v0.3.0"`
	CommodoreCacheNegativeTTL time.Duration `env:"COMMODORE_CACHE_NEG_TTL" default:"10s" desc:"Lifetime of cached Commodore not-found responses." introduced:"v0.3.0"`
	CommodoreCacheMaxEntries  int           `env:"COMMODORE_CACHE_MAX" default:"10000" desc:"Maximum cached Commodore responses. 0 or less uses 10000." introduced:"v0.3.0"`

	GeoIPCacheTTL         time.Duration `env:"GEOIP_CACHE_TTL" default:"300s" desc:"Freshness window of cached GeoIP lookups when a GeoIP database is loaded." introduced:"v0.3.0"`
	GeoIPCacheSWR         time.Duration `env:"GEOIP_CACHE_SWR" default:"120s" desc:"Stale-while-revalidate window of cached GeoIP lookups." introduced:"v0.3.0"`
	GeoIPCacheNegativeTTL time.Duration `env:"GEOIP_CACHE_NEG_TTL" default:"60s" desc:"Lifetime of cached failed GeoIP lookups." introduced:"v0.3.0"`
	GeoIPCacheMaxEntries  int           `env:"GEOIP_CACHE_MAX" default:"50000" desc:"Maximum cached GeoIP lookups. 0 or less uses 50000." introduced:"v0.3.0"`

	StreamCacheSWR        string `env:"STREAM_CACHE_SWR" default:"30s" desc:"Stale-while-revalidate duration of the trigger stream context cache. Values that are not durations use 30s." introduced:"v0.3.0"`
	BillingCacheSWR       string `env:"BILLING_CACHE_SWR" default:"5m" desc:"Stale-while-revalidate duration of the tenant billing decision cache. Values that are not durations use 5m." introduced:"v0.3.0"`
	BillingDeniedCacheTTL string `env:"BILLING_DENIED_CACHE_TTL" default:"30s" desc:"Lifetime of a cached denied billing decision. Values that are not positive durations use 30s." introduced:"v0.3.0"`
	RegistryStaleMax      string `env:"FOGHORN_REGISTRY_STALE_MAX" desc:"How long past expiry a stream registry entry may be served while re-resolution fails transiently. 0 disables stale serving; empty keeps 5m; invalid or negative values log a warning and keep 5m." introduced:"v0.3.0"`
}

// FoghornStorage is the cell's S3 cold storage.
type FoghornStorage struct {
	S3Bucket    string `env:"STORAGE_S3_BUCKET" desc:"S3 bucket for durable artifacts and thumbnails. When set, S3 must initialize and match the cell's committed backend identity or startup fails." introduced:"v0.3.0"`
	S3Prefix    string `env:"STORAGE_S3_PREFIX" desc:"Key prefix inside STORAGE_S3_BUCKET for this cell." introduced:"v0.3.0"`
	S3Region    string `env:"STORAGE_S3_REGION" default:"us-east-1" desc:"Region of STORAGE_S3_BUCKET." introduced:"v0.3.0"`
	S3Endpoint  string `env:"STORAGE_S3_ENDPOINT" desc:"S3-compatible endpoint URL. Empty uses the AWS endpoint for the region." introduced:"v0.3.0"`
	S3AccessKey string `env:"STORAGE_S3_ACCESS_KEY" secret:"true" desc:"Access key ID for STORAGE_S3_BUCKET. Credentials stay in Foghorn; edge nodes receive presigned URLs." introduced:"v0.3.0"`
	S3SecretKey string `env:"STORAGE_S3_SECRET_KEY" secret:"true" desc:"Secret access key for STORAGE_S3_ACCESS_KEY." introduced:"v0.3.0"`
	StorageMode string `env:"STORAGE_MODE" desc:"Set to none to start a storage-less cell without STORAGE_S3_BUCKET. Otherwise a storage-less start requires Quartermaster to confirm the cluster has no S3 backend." introduced:"v0.3.0"`

	DefaultStorageBase string `env:"FOGHORN_DEFAULT_STORAGE_BASE" desc:"Absolute local storage path used to rebuild DVR paths for nodes that report none. Must match HELMSMAN_STORAGE_LOCAL_PATH." introduced:"v0.3.0"`
}

// FoghornMediaAuthority configures signed media authority apply.
type FoghornMediaAuthority struct {
	MediaAuthorityCellID               string `env:"MEDIA_AUTHORITY_CELL_ID" desc:"Control cell whose signed media authority this instance applies and requests replays for. Empty uses CLUSTER_ID; required when MEDIA_AUTHORITY_TRUST_SET is set." introduced:"v0.3.0"`
	MediaAuthorityTrustSet             string `env:"MEDIA_AUTHORITY_TRUST_SET" desc:"JSON object mapping trusted media authority signer key IDs to base64 raw Ed25519 public keys. Empty disables signed media authority apply and placement enforcement." introduced:"v0.3.0"`
	MediaAuthoritySealKeyID            string `env:"MEDIA_AUTHORITY_SEAL_KEY_ID" desc:"Key ID of the private key that opens sealed secrets in media authority. Set together with MEDIA_AUTHORITY_SEAL_PRIVATE_KEY_PEM_B64." introduced:"v0.3.0"`
	MediaAuthoritySealPrivateKeyPEMB64 string `env:"MEDIA_AUTHORITY_SEAL_PRIVATE_KEY_PEM_B64" secret:"true" desc:"Base64-wrapped PKCS#8 PRIVATE KEY PEM that opens sealed secrets in media authority." introduced:"v0.3.0"`
}

// FoghornAdmission tunes load-based admission, the ingest resolver rate
// limit, and Livepeer VOD dispatch. Its string fields are parsed where they
// are used and fall back to their defaults on values that do not parse.
type FoghornAdmission struct {
	IngestRejectOverAllowanceLoad string `env:"FOGHORN_INGEST_REJECT_OVER_ALLOWANCE_LOAD" default:"0.5" desc:"Cluster load between 0 and 1 above which over-allowance free-tier ingest is rejected. Values outside the open range use 0.5." introduced:"v0.3.0"`
	IngestRejectFreeLoad          string `env:"FOGHORN_INGEST_REJECT_FREE_LOAD" default:"0.95" desc:"Cluster load between 0 and 1 above which any free-tier ingest is rejected. Values outside the open range use 0.95." introduced:"v0.3.0"`
	ViewerRejectOverAllowanceLoad string `env:"FOGHORN_VIEWER_REJECT_OVER_ALLOWANCE_LOAD" default:"0.8" desc:"Cluster load between 0 and 1 above which over-allowance free-tier viewers are rejected. Values outside the open range use 0.8." introduced:"v0.3.0"`
	ViewerRejectFreeLoad          string `env:"FOGHORN_VIEWER_REJECT_FREE_LOAD" default:"0.95" desc:"Cluster load between 0 and 1 above which any free-tier viewer is rejected. Values outside the open range use 0.95." introduced:"v0.3.0"`

	IngestResolveRatePerMinute string `env:"INGEST_RESOLVE_RATE_PER_MIN" default:"60" desc:"Per-client-IP refill rate of the unauthenticated ingest resolver, in requests per minute. Re-read on every request; values below 1 use 1." introduced:"v0.3.0"`
	IngestResolveBurst         string `env:"INGEST_RESOLVE_BURST" default:"10" desc:"Per-client-IP burst of the ingest resolver rate limit. Re-read on every request; values below 1 use 1." introduced:"v0.3.0"`
	IngestResolveMaxBuckets    string `env:"INGEST_RESOLVE_MAX_BUCKETS" default:"50000" desc:"Maximum client IPs tracked by the ingest resolver rate limit before the least recently used is evicted." introduced:"v0.3.0"`
	TrustedProxyCIDRs          string `env:"TRUSTED_PROXY_CIDRS" desc:"Comma-separated CIDRs or IPs of reverse proxies whose forwarding headers identify the client for rate limiting and routing. Re-read on every request." introduced:"v0.3.0"`

	LivepeerVODDeadlineMS string `env:"LIVEPEER_VOD_DEADLINE_MS" default:"30000" desc:"Per-segment gateway response budget in milliseconds stamped onto VOD Livepeer process configs. Values that are not positive integers use 30000." introduced:"v0.3.0"`
	LivepeerVODMinSpeed   string `env:"LIVEPEER_VOD_MIN_SPEED" default:"0.5" desc:"Minimum sustained speed factor stamped onto VOD Livepeer process configs. Values that are not positive numbers use 0.5." introduced:"v0.3.0"`

	DVRClusterMaxWindowSeconds int `env:"DVR_CLUSTER_MAX_WINDOW_SECONDS" default:"0" desc:"Cluster ceiling for DVR window length in seconds, applied on top of tier ceilings. 0 applies no cluster ceiling." introduced:"v0.3.0"`
	DVRClusterMaxEntries       int `env:"DVR_CLUSTER_MAX_ENTRIES" default:"0" desc:"Cluster ceiling for DVR playlist entries, applied on top of tier ceilings. 0 applies no cluster ceiling." introduced:"v0.3.0"`
}

// FoghornChandler locates the Chandler asset service.
type FoghornChandler struct {
	ChandlerBaseURL     string `env:"CHANDLER_BASE_URL" desc:"Single local Chandler origin for every asset URL. Empty derives per-cluster Chandler origins from Quartermaster." introduced:"v0.3.0"`
	ChandlerInternalURL string `env:"CHANDLER_INTERNAL_URL" desc:"Comma-separated in-cell Chandler base URLs that receive thumbnail cache invalidations. Empty uses the Chandler base URL." introduced:"v0.3.0"`
	ChandlerHost        string `env:"CHANDLER_HOST" default:"chandler" desc:"Chandler host for the http fallback origin when no base URL is configured or derived." introduced:"v0.3.0"`
	ChandlerPort        string `env:"CHANDLER_PORT" default:"18020" desc:"Chandler port for the http fallback origin." introduced:"v0.3.0"`
}

// Foghorn is the startup configuration of the Foghorn server. Main installs a
// config.Live of it with Install; values that internal packages read through
// Current at use time follow a SIGHUP env-file reload.
//
//configref:service foghorn cmd=cmd/foghorn
type Foghorn struct {
	config.HTTPListen
	config.HTTPRuntime
	config.Logging
	config.SystemTenant
	config.GeoIP
	config.GRPCTLS
	config.Postgres
	config.BuildEnvironment
	FoghornRedis
	FoghornListeners
	FoghornClients
	FoghornCaches
	FoghornStorage
	FoghornMediaAuthority
	FoghornAdmission
	FoghornChandler

	ServiceToken string `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Shared service-to-service bearer token for control-plane clients, internal gRPC, internal HTTP reads, the debug surface, and Chandler invalidations." introduced:"v0.3.0"`
	JWTSecret    string `env:"JWT_SECRET" secret:"true" desc:"HMAC secret that verifies platform operator JWTs on internal HTTP routes and node lifecycle gRPC methods. Empty leaves those routes to the service token only." introduced:"v0.3.0"`
	// MetadataPolicy defaults to deny: Foghorn methods that act for a tenant or
	// user authenticate and reconstruct that identity themselves, so ambient
	// metadata on a shared service credential is never trusted.
	MetadataPolicy string `env:"GRPC_METADATA_POLICY" default:"deny" desc:"Handling of x-user-id and x-tenant-id metadata on gRPC calls authenticated with the service token: allow applies them to the request, audit applies and logs them, deny ignores them and logs a warning." introduced:"v0.3.0"`
	// BalancerCapabilitySecret is required at startup: without it Livepeer
	// dispatches would be minted or rejected only at request time and every
	// ingest would fail as an opaque 403.
	BalancerCapabilitySecret         string `env:"FOGHORN_BALANCER_CAPABILITY_SECRET" required:"true" secret:"true" desc:"HMAC secret that signs and verifies Mist balancer, source pull, processing source, and Livepeer transcode job capabilities." introduced:"v0.3.0"`
	StateEncryptionKey               string `env:"FOGHORN_STATE_ENCRYPTION_KEY" required:"true" secret:"true" desc:"Secret from which the key that encrypts durable ingest admission push-target payloads is derived." introduced:"v0.3.0"`
	EdgeTelemetryJWTPrivateKeyPEMB64 string `env:"EDGE_TELEMETRY_JWT_PRIVATE_KEY_PEM_B64" secret:"true" desc:"Base64-wrapped EC private key PEM, SEC 1 or PKCS#8, that signs edge telemetry write tokens. Empty sends edge nodes no telemetry configuration." introduced:"v0.3.0"`

	ClusterID  string `env:"CLUSTER_ID" desc:"Media cluster this instance serves. Empty uses default for local state, registration, and storage identity; events and trigger enrichment carry the value as set." introduced:"v0.3.0"`
	NodeID     string `env:"NODE_ID" desc:"Node this instance registers under in Quartermaster and records in trigger events." introduced:"v0.3.0"`
	Region     string `env:"REGION" desc:"Region label attached to events sent to Decklog." introduced:"v0.3.0"`
	InstanceID string `env:"FOGHORN_INSTANCE_ID" desc:"Stable replica identity for HA state sync, leases, and served cluster assignments. Empty generates a per-process identity and skips served cluster assignment loading." introduced:"v0.3.0"`

	PlatformSharedClusters []string `env:"FOGHORN_PLATFORM_SHARED_CLUSTERS" desc:"Comma-separated cluster IDs whose tenantless nodes may serve any tenant's durable media, in addition to Quartermaster platform-official clusters." introduced:"v0.3.0"`
	PlatformRootDomain     string   `env:"BRAND_DOMAIN" default:"frameworks.network" desc:"Platform root domain for cluster-scoped Foghorn, edge, telemetry, and certificate host names." introduced:"v0.3.0"`
	ACMEEmail              string   `env:"ACME_EMAIL" desc:"Contact email sent to edge nodes in their site configuration for ACME certificate issuance." introduced:"v0.3.0"`

	FederationEnabled          string `env:"FEDERATION_ENABLED" default:"false" desc:"Enables cross-cluster federation when exactly true; any other value leaves it off. Requires Quartermaster, an active platform-official cluster, Redis, and authenticated TLS." introduced:"v0.3.0"`
	FederationAllowInsecureDev bool   `env:"FEDERATION_ALLOW_INSECURE_DEV" default:"false" desc:"Allows federation together with GRPC_ALLOW_INSECURE. Isolated development only." introduced:"v0.3.0"`

	CPUWeight       int `env:"CPU_WEIGHT" default:"500" desc:"Load balancer CPU score weight. The five balancer weights apply only when all are greater than 0." introduced:"v0.3.0"`
	RAMWeight       int `env:"RAM_WEIGHT" default:"500" desc:"Load balancer memory score weight." introduced:"v0.3.0"`
	BandwidthWeight int `env:"BANDWIDTH_WEIGHT" default:"1000" desc:"Load balancer bandwidth score weight." introduced:"v0.3.0"`
	GeoWeight       int `env:"GEO_WEIGHT" default:"1000" desc:"Load balancer geographic distance score weight." introduced:"v0.3.0"`
	StreamBonus     int `env:"STREAM_BONUS" default:"50" desc:"Load balancer score bonus for a node that already carries the stream." introduced:"v0.3.0"`

	EdgeReleaseReconcileIntervalSeconds int `env:"EDGE_RELEASE_RECONCILE_INTERVAL_SECONDS" default:"60" desc:"Seconds between edge release reconciliation passes against Quartermaster." introduced:"v0.3.0"`
	RestartReconnectWindowSeconds       int `env:"FOGHORN_RESTART_RECONNECT_WINDOW_SECONDS" default:"20" desc:"Seconds an announced node restart keeps node health before the disconnect counts as unhealthy, clamped to 5 through 30. Read at startup." introduced:"v0.3.0"`
}

// LocalClusterID returns CLUSTER_ID, or default when it is empty.
// Federation reports whether cross-cluster federation is enabled. Only the
// exact value "true" enables it; any other value leaves it off.
func (c *Foghorn) Federation() bool {
	return c.FederationEnabled == "true"
}

func (c *Foghorn) LocalClusterID() string {
	if c.ClusterID == "" {
		return "default"
	}
	return c.ClusterID
}

// ControlCellID returns MEDIA_AUTHORITY_CELL_ID, or the local cluster ID when
// it is empty.
func (c *Foghorn) ControlCellID() string {
	if c.MediaAuthorityCellID == "" {
		return c.LocalClusterID()
	}
	return c.MediaAuthorityCellID
}

// IsProduction reports whether BUILD_ENV is production or prod.
func (c *Foghorn) IsProduction() bool {
	switch strings.ToLower(c.BuildEnv) {
	case "production", "prod":
		return true
	default:
		return false
	}
}

// Validate enforces the storage base path shape and the signed media
// authority settings that must accompany a trust set.
func (c *Foghorn) Validate() error {
	var errs []error
	for key, addr := range map[string]string{
		"FOGHORN_INTERNAL_GRPC_BIND_ADDR": c.InternalGRPCBindAddr,
		"FOGHORN_EXTERNAL_GRPC_BIND_ADDR": c.ExternalGRPCBindAddr,
	} {
		if addr == "" {
			continue
		}
		if _, port, err := net.SplitHostPort(addr); err != nil || port == "" {
			errs = append(errs, fmt.Errorf("%s must be host:port or :port", key))
		}
	}
	if err := config.ValidateMetadataPolicy(c.MetadataPolicy); err != nil {
		errs = append(errs, err)
	}
	if c.InternalGRPCPort < 0 || c.InternalGRPCPort > 65535 {
		errs = append(errs, errors.New("FOGHORN_INTERNAL_GRPC_PORT must be between 0 and 65535"))
	}
	if c.DefaultStorageBase != "" && !filepath.IsAbs(c.DefaultStorageBase) {
		errs = append(errs, errors.New("FOGHORN_DEFAULT_STORAGE_BASE must be an absolute path"))
	}
	if c.MediaAuthorityTrustSet != "" {
		if c.MediaAuthorityCellID == "" {
			errs = append(errs, errors.New("MEDIA_AUTHORITY_CELL_ID is required when MEDIA_AUTHORITY_TRUST_SET is set"))
		}
		if (c.MediaAuthoritySealKeyID == "") != (c.MediaAuthoritySealPrivateKeyPEMB64 == "") {
			errs = append(errs, errors.New("MEDIA_AUTHORITY_SEAL_KEY_ID and MEDIA_AUTHORITY_SEAL_PRIVATE_KEY_PEM_B64 must be configured together"))
		}
	}
	return errors.Join(errs...)
}

// FoghornDataMigrations is the configuration of `foghorn data-migrations`.
//
//configref:service foghorn cmd=cmd/foghorn variant=data-migrations
type FoghornDataMigrations struct {
	config.Postgres
}
