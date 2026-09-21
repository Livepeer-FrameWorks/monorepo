// Package appconfig holds the typed configuration of the Helmsman binary and
// its subcommands. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// ServiceID is the pkg/servicedefs ID of Helmsman.
const ServiceID = "helmsman"

// Fallbacks for values that cannot be parsed. Customer-hosted edges are
// rendered outside this repository, so an unparseable value keeps the legacy
// fallback instead of failing startup; TestLegacyFallbacksMatchTagDefaults
// keeps these in step with the tag defaults.
const (
	fallbackBlockingGraceMs          = 2000
	fallbackRotateNodeIdentity       = false
	fallbackGRPCAllowInsecure        = false
	fallbackCapabilityEnabled        = true
	fallbackStorageThresholdUnparsed = 0
)

// EdgeRuntime is the process environment that Helmsman's internal packages
// read at use time. Read through Runtime, it follows a SIGHUP env-file reload.
// Every field is a string: the legacy readers tolerated malformed values, so
// decoding never fails and each call site keeps its own parse.
type EdgeRuntime struct {
	MistAPIUsername string `env:"MIST_API_USERNAME" default:"test" desc:"MistServer API user for every Helmsman MistServer API client." introduced:"v0.3.0"`
	MistAPIPassword string `env:"MIST_API_PASSWORD" default:"test" secret:"true" desc:"Password for MIST_API_USERNAME." introduced:"v0.3.0"`

	StorageLocalPath     string `env:"HELMSMAN_STORAGE_LOCAL_PATH" desc:"Local media storage root. Empty disables the storage manager, artifact relay, and artifact scans; artifact path lookups then use /var/lib/frameworks/edge-storage." introduced:"v0.3.0"`
	StorageS3Bucket      string `env:"HELMSMAN_STORAGE_S3_BUCKET" desc:"Cold storage bucket name reported to Foghorn in node registration and lifecycle updates. Helmsman holds no storage credentials." introduced:"v0.3.0"`
	StorageS3Prefix      string `env:"HELMSMAN_STORAGE_S3_PREFIX" desc:"Cold storage key prefix reported to Foghorn in node registration and lifecycle updates." introduced:"v0.3.0"`
	StorageCapacityBytes string `env:"HELMSMAN_STORAGE_CAPACITY_BYTES" default:"0" desc:"Logical storage capacity in bytes, reported to Foghorn and applied to disk-space accounting. 0 or a value that is not an unsigned integer uses the filesystem size." introduced:"v0.3.0"`

	CapIngest     string `env:"HELMSMAN_CAP_INGEST" default:"true" desc:"Advertises the ingest role. Registration treats a value that is not a boolean as true; node lifecycle updates count only true or 1 as enabled." introduced:"v0.3.0"`
	CapEdge       string `env:"HELMSMAN_CAP_EDGE" default:"true" desc:"Advertises the edge playback role. Registration treats a value that is not a boolean as true; node lifecycle updates count only true or 1 as enabled." introduced:"v0.3.0"`
	CapStorage    string `env:"HELMSMAN_CAP_STORAGE" default:"true" desc:"Advertises the storage role. Registration treats a value that is not a boolean as true; node lifecycle updates count only true or 1 as enabled." introduced:"v0.3.0"`
	CapProcessing string `env:"HELMSMAN_CAP_PROCESSING" default:"true" desc:"Advertises the processing role. Registration treats a value that is not a boolean as true; node lifecycle updates count only true or 1 as enabled." introduced:"v0.3.0"`
	MaxTranscodes string `env:"HELMSMAN_MAX_TRANSCODES" default:"0" desc:"Video transcode slots a processing node advertises. 0 or a value that is not an integer advertises no ceiling." introduced:"v0.3.0"`

	MistWebhookBaseURL string `env:"HELMSMAN_WEBHOOK_URL" default:"http://localhost:18007" desc:"Base URL of the Helmsman webhook routes that the managed MistServer trigger configuration points at." introduced:"v0.3.0"`
	RelayBaseURL       string `env:"HELMSMAN_RELAY_BASE_URL" default:"http://127.0.0.1:18007" desc:"URL MistServer on this node uses to reach the Helmsman artifact relay. Set it only when MistServer reaches Helmsman through a service name." introduced:"v0.3.0"`

	GRPCTLSCAPath    string `env:"GRPC_TLS_CA_PATH" desc:"CA bundle that verifies Foghorn on the control connection and where Helmsman writes the CA bundle delivered in the config seed. Empty uses the system roots for the connection and /etc/frameworks/pki/ca.crt for the written bundle." introduced:"v0.3.0"`
	EdgeTLSCertPath  string `env:"HELMSMAN_TLS_CERT_PATH" default:"/etc/frameworks/certs/cert.pem" desc:"Path where Helmsman writes the edge TLS certificate delivered in the config seed." introduced:"v0.3.0"`
	EdgeTLSKeyPath   string `env:"HELMSMAN_TLS_KEY_PATH" default:"/etc/frameworks/certs/key.pem" desc:"Path where Helmsman writes the private key for HELMSMAN_TLS_CERT_PATH." introduced:"v0.3.0"`
	EdgeTLSBundleDir string `env:"HELMSMAN_TLS_BUNDLE_DIR" default:"/etc/frameworks/certs/bundles" desc:"Directory where Helmsman writes the per-bundle TLS certificate and key files delivered in the config seed." introduced:"v0.3.0"`

	CaddyTLSGroup    string `env:"CADDY_TLS_GROUP" desc:"Group name or numeric ID given to written TLS files so Caddy can read them. Empty tries the caddy group, then the group of the parent directory." introduced:"v0.3.0"`
	CaddyAdminSocket string `env:"CADDY_ADMIN_SOCKET" desc:"Unix socket of the Caddy admin API used for config reloads and restarts. Takes precedence over CADDY_ADMIN_URL." introduced:"v0.3.0"`
	CaddyAdminURL    string `env:"CADDY_ADMIN_URL" desc:"Caddy admin API address used when CADDY_ADMIN_SOCKET is empty. Empty uses localhost:2019." introduced:"v0.3.0"`
	CaddyConfigPath  string `env:"CADDY_CONFIG_PATH" default:"/etc/caddy/Caddyfile" desc:"Caddyfile that Helmsman writes and posts to the Caddy admin API on reload." introduced:"v0.3.0"`
	ChandlerUpstream string `env:"CHANDLER_URL" default:"chandler:18020" desc:"Chandler host:port that the Caddyfile rendered from the config seed reverse-proxies to." introduced:"v0.3.0"`
	MistHTTPUpstream string `env:"MISTSERVER_HTTP_URL" default:"http://mistserver:8080" desc:"MistServer HTTP output URL that the rendered Caddyfile reverse-proxies to. An http:// prefix is stripped." introduced:"v0.3.0"`

	BandwidthLimitBytesPerSec string `env:"HELMSMAN_BW_LIMIT_BYTES_PER_SEC" desc:"Operator-pinned node bandwidth limit in bytes per second, written to MistServer and reported to Foghorn. Takes precedence over the other bandwidth limit keys." introduced:"v0.3.0"`
	BandwidthLimitBytes       string `env:"HELMSMAN_BW_LIMIT_BYTES" desc:"Node bandwidth limit in bytes per second, used when HELMSMAN_BW_LIMIT_BYTES_PER_SEC is empty or 0." introduced:"v0.3.0"`
	BandwidthLimitMbps        string `env:"HELMSMAN_BW_LIMIT_MBPS" desc:"Node bandwidth limit in megabits per second, used when neither byte-based bandwidth limit key is set." introduced:"v0.3.0"`

	Supervisor      string `env:"HELMSMAN_SUPERVISOR" desc:"Process supervisor used for in-place component updates: s6, systemd, or launchd. Any other value detects s6 supervision, then uses launchd on macOS and systemd elsewhere." introduced:"v0.3.0"`
	DeployMode      string `env:"DEPLOY_MODE" default:"native" desc:"Deployment mode reported to Foghorn. A value other than native without s6 supervision refuses in-place component updates." introduced:"v0.3.0"`
	MistONNXProfile string `env:"MIST_ONNX_PROFILE" default:"cpu" desc:"ONNX runtime profile of the bundled MistServer, reported to Foghorn in node lifecycle updates." introduced:"v0.3.0"`

	TriggerWALDir             string `env:"FRAMEWORKS_TRIGGER_WAL_DIR" desc:"Directory of the durable Mist trigger WAL. Empty uses trigger-wal under HELMSMAN_STATE_DIR." introduced:"v0.3.0"`
	IngestGenerationStorePath string `env:"FRAMEWORKS_INGEST_GENERATION_STORE_PATH" desc:"Directory of the ingest generation fence store. Empty uses ingest-generation-fences under HELMSMAN_STATE_DIR." introduced:"v0.3.0"`
	ControlOutboxDir          string `env:"FRAMEWORKS_CONTROL_OUTBOX_DIR" desc:"Directory of the durable control outbox. Empty uses control-outbox under HELMSMAN_STATE_DIR, or the user cache directory when that is empty." introduced:"v0.3.0"`

	RestreamAllowPrivateDestinations string `env:"RESTREAM_ALLOW_PRIVATE_DESTINATIONS" desc:"Allows multistream destinations that resolve to private addresses when set to true or 1." introduced:"v0.3.0"`
	RestreamAllowedPrivateCIDRs      string `env:"RESTREAM_ALLOWED_PRIVATE_CIDRS" desc:"Comma-separated CIDRs exempted from the private, loopback, and link-local multistream destination block." introduced:"v0.3.0"`
	RestreamDeniedCIDRs              string `env:"RESTREAM_DENIED_CIDRS" desc:"Comma-separated CIDRs never allowed as multistream destinations. Denials win over allowances. An invalid entry fails startup and, after a reload, push target activation." introduced:"v0.3.0"`

	HelmsmanVersion               string `env:"HELMSMAN_VERSION" desc:"Helmsman version reported to Foghorn when no component version has been recorded on disk." introduced:"v0.3.0"`
	MistVersion                   string `env:"MIST_VERSION" desc:"MistServer version reported to Foghorn when no component version has been recorded on disk." introduced:"v0.3.0"`
	MistServerVersion             string `env:"MISTSERVER_VERSION" desc:"MistServer version reported to Foghorn when MIST_VERSION and the recorded versions are empty." introduced:"v0.3.0"`
	CaddyVersion                  string `env:"CADDY_VERSION" desc:"Caddy version reported to Foghorn when no component version has been recorded on disk." introduced:"v0.3.0"`
	ConfigSchemaVersion           string `env:"CONFIG_SCHEMA_VERSION" desc:"Edge configuration schema version reported to Foghorn when no version has been recorded on disk." introduced:"v0.3.0"`
	FrameworksConfigSchemaVersion string `env:"FRAMEWORKS_CONFIG_SCHEMA_VERSION" desc:"Edge configuration schema version reported to Foghorn when CONFIG_SCHEMA_VERSION and the recorded versions are empty." introduced:"v0.3.0"`
}

// Helmsman is the configuration of the Helmsman serve process. Fields outside
// EdgeRuntime apply at startup, except the identity fields read at use through
// NodeID, MistServerURL, EdgePublicURL, and StateDir.
//
//configref:service helmsman cmd=cmd/helmsman
type Helmsman struct {
	config.HTTPListen
	config.HTTPRuntime
	config.TolerantLogging
	EdgeRuntime

	NodeID             string `env:"NODE_ID" required:"true" desc:"Node identity registered with Foghorn and stamped on triggers. A persisted config seed that belongs to another node is refused." introduced:"v0.3.0"`
	FoghornControlAddr string `env:"FOGHORN_CONTROL_ADDR" required:"true" desc:"Foghorn control-stream gRPC address. Helmsman dials it at startup and reconnects with backoff while the node keeps serving." introduced:"v0.3.0"`
	MistServerURL      string `env:"MISTSERVER_URL" required:"true" desc:"Local MistServer API base URL used for the health check, the Mist admin proxy, stream control, and DTSH generation." introduced:"v0.3.0"`
	EdgePublicURL      string `env:"EDGE_PUBLIC_URL" required:"true" desc:"Client-facing edge URL reported to Foghorn as the playback base URL and written to the MistServer protocol public addresses." introduced:"v0.3.0"`
	StateDir           string `env:"HELMSMAN_STATE_DIR" required:"true" desc:"Durable sidecar state directory for node identity, the persisted config seed, the trigger WAL, the control outbox, and ingest generation fences." introduced:"v0.3.0"`

	FoghornGRPCTLSServerName string `env:"FOGHORN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Foghorn control connection. Empty uses the host of a public FOGHORN_CONTROL_ADDR, otherwise the canonical internal name." introduced:"v0.3.0"`
	GRPCTLSCertPath          string `env:"GRPC_TLS_CERT_PATH" desc:"Client certificate presented on the Foghorn control connection. Used only together with GRPC_TLS_KEY_PATH." introduced:"v0.3.0"`
	GRPCTLSKeyPath           string `env:"GRPC_TLS_KEY_PATH" desc:"Private key for GRPC_TLS_CERT_PATH." introduced:"v0.3.0"`
	GRPCAllowInsecure        string `env:"GRPC_ALLOW_INSECURE" default:"false" desc:"Allows a plaintext Foghorn control connection when no TLS material is configured and the address is not a fully qualified name. A value that is not a boolean counts as false." introduced:"v0.3.0"`

	EnrollmentToken     string `env:"EDGE_ENROLLMENT_TOKEN" secret:"true" desc:"Enrollment token presented to Foghorn when the node has no durable identity or rotates it." introduced:"v0.3.0"`
	RotateNodeIdentity  string `env:"HELMSMAN_ROTATE_NODE_IDENTITY" default:"false" desc:"Requests a node identity rotation with EDGE_ENROLLMENT_TOKEN at startup. A value that is not a boolean counts as false." introduced:"v0.3.0"`
	EnrollmentTokenFile string `env:"HELMSMAN_ENROLLMENT_TOKEN_FILE" desc:"Env file whose EDGE_ENROLLMENT_TOKEN is cleared after Foghorn accepts the enrollment." introduced:"v0.3.0"`
	RuntimeEnvFile      string `env:"HELMSMAN_RUNTIME_ENV_FILE" desc:"Env file whose HELMSMAN_ROTATE_NODE_IDENTITY is cleared after Foghorn accepts the enrollment." introduced:"v0.3.0"`

	StorageFreezeThreshold   string `env:"HELMSMAN_FREEZE_THRESHOLD" default:"0.85" desc:"Disk usage fraction at which the storage manager starts freezing local artifacts to cold storage. A value that is not a number counts as 0." introduced:"v0.3.0"`
	StorageTargetAfterFreeze string `env:"HELMSMAN_TARGET_AFTER_FREEZE" default:"0.70" desc:"Disk usage fraction the storage manager frees space down to once freezing starts. A value that is not a number counts as 0." introduced:"v0.3.0"`
	RelayTrustedCIDR         string `env:"HELMSMAN_RELAY_TRUSTED_CIDR" desc:"Comma-separated CIDRs whose direct peers reach the artifact relay like loopback. Invalid entries are ignored with a warning. Leave empty unless MistServer reaches Helmsman over a non-loopback address." introduced:"v0.3.0"`

	BlockingGraceMs          string `env:"HELMSMAN_BLOCKING_GRACE_MS" default:"2000" desc:"Milliseconds a blocking Mist trigger waits for the Foghorn control stream to reconnect before failing. A value that is not an integer uses 2000." introduced:"v0.3.0"`
	RequestedOperationalMode string `env:"HELMSMAN_OPERATIONAL_MODE" default:"normal" desc:"Operational mode requested at registration: normal, draining, or maintenance. Foghorn stays authoritative and may override it." introduced:"v0.3.0"`

	PublicBindAddr     string `env:"HELMSMAN_BIND_ADDR" desc:"Bind address of the edge API, MistServer webhook, and artifact relay listener on PORT. Empty binds all interfaces." introduced:"v0.3.0"`
	ManagementPort     string `env:"HELMSMAN_MANAGEMENT_PORT" default:"18017" desc:"TCP port of the management listener that serves node mode, trigger WAL, and Prometheus node routes." introduced:"v0.3.0"`
	ManagementBindAddr string `env:"HELMSMAN_MANAGEMENT_BIND_ADDR" default:"127.0.0.1" desc:"Bind address of the management listener. Must be a loopback address or localhost." introduced:"v0.3.0"`
}

// HelmsmanScrubEdgeCredentials is the configuration of the
// scrub-edge-credentials subcommand, which clears consumed enrollment
// credentials from the node env files.
//
//configref:service helmsman cmd=cmd/helmsman variant=scrub-edge-credentials
type HelmsmanScrubEdgeCredentials struct {
	StateDir            string `env:"HELMSMAN_STATE_DIR" desc:"Durable sidecar state directory that holds the credential cleanup request and completion markers." introduced:"v0.3.0"`
	NodeID              string `env:"NODE_ID" desc:"Node identity whose accepted enrollment the cleanup request must match." introduced:"v0.3.0"`
	EnrollmentTokenFile string `env:"HELMSMAN_ENROLLMENT_TOKEN_FILE" desc:"Env file whose EDGE_ENROLLMENT_TOKEN is cleared." introduced:"v0.3.0"`
	RuntimeEnvFile      string `env:"HELMSMAN_RUNTIME_ENV_FILE" desc:"Env file whose HELMSMAN_ROTATE_NODE_IDENTITY is cleared." introduced:"v0.3.0"`
}

// Validate rejects a management listener that does not bind loopback.
func (c *Helmsman) Validate() error {
	if !IsLoopbackBind(c.ManagementBindAddr) {
		return errors.New("HELMSMAN_MANAGEMENT_BIND_ADDR must be a loopback address or localhost")
	}
	return nil
}

// IsLoopbackBind reports whether bindAddr is localhost or a loopback IP,
// with or without IPv6 brackets.
func IsLoopbackBind(bindAddr string) bool {
	bindAddr = strings.TrimSpace(strings.Trim(bindAddr, "[]"))
	if strings.EqualFold(bindAddr, "localhost") {
		return true
	}
	ip := net.ParseIP(bindAddr)
	return ip != nil && ip.IsLoopback()
}

// BlockingGrace returns HELMSMAN_BLOCKING_GRACE_MS in milliseconds.
func (c *Helmsman) BlockingGrace() int {
	return intOr(c.BlockingGraceMs, fallbackBlockingGraceMs)
}

// RotateNodeIdentityRequested reports HELMSMAN_ROTATE_NODE_IDENTITY.
func (c *Helmsman) RotateNodeIdentityRequested() bool {
	return boolOr(c.RotateNodeIdentity, fallbackRotateNodeIdentity)
}

// GRPCInsecureAllowed reports GRPC_ALLOW_INSECURE.
func (c *Helmsman) GRPCInsecureAllowed() bool {
	return boolOr(c.GRPCAllowInsecure, fallbackGRPCAllowInsecure)
}

// StorageThresholds returns the freeze and target-after-freeze fractions.
func (c *Helmsman) StorageThresholds() (freeze, targetAfterFreeze float64) {
	return floatOr(c.StorageFreezeThreshold, fallbackStorageThresholdUnparsed), floatOr(c.StorageTargetAfterFreeze, fallbackStorageThresholdUnparsed)
}

// Capabilities returns the registration capability flags: a value that is not
// a boolean counts as enabled.
func (r EdgeRuntime) Capabilities() (ingest, edge, storage, processing bool) {
	return boolOr(r.CapIngest, fallbackCapabilityEnabled), boolOr(r.CapEdge, fallbackCapabilityEnabled),
		boolOr(r.CapStorage, fallbackCapabilityEnabled), boolOr(r.CapProcessing, fallbackCapabilityEnabled)
}

// MaxTranscodeSlots returns HELMSMAN_MAX_TRANSCODES, or 0 when it is not an
// integer.
func (r EdgeRuntime) MaxTranscodeSlots() int {
	return intOr(r.MaxTranscodes, 0)
}

// StorageCapacity returns HELMSMAN_STORAGE_CAPACITY_BYTES, or 0 when it is not
// an unsigned integer.
func (r EdgeRuntime) StorageCapacity() uint64 {
	return uintOr(r.StorageCapacityBytes)
}

// StoragePathOrDefault returns HELMSMAN_STORAGE_LOCAL_PATH, or the durable
// edge storage root when it is empty.
func (r EdgeRuntime) StoragePathOrDefault() string {
	if r.StorageLocalPath == "" {
		return "/var/lib/frameworks/edge-storage"
	}
	return r.StorageLocalPath
}

// BandwidthLimitBytesPerSecond returns the operator-pinned bandwidth limit in
// bytes per second, or 0 when none of the bandwidth keys holds a positive
// integer.
func (r EdgeRuntime) BandwidthLimitBytesPerSecond() uint64 {
	if v := uintOr(r.BandwidthLimitBytesPerSec); v > 0 {
		return v
	}
	if v := uintOr(r.BandwidthLimitBytes); v > 0 {
		return v
	}
	if mbps := uintOr(r.BandwidthLimitMbps); mbps > 0 {
		return mbps * 1000 * 1000 / 8
	}
	return 0
}

// ComponentVersionEnv returns the environment-provided version for a component
// version key, or empty for a key Helmsman does not declare.
func (r EdgeRuntime) ComponentVersionEnv(key string) string {
	switch key {
	case "HELMSMAN_VERSION":
		return r.HelmsmanVersion
	case "MIST_VERSION":
		return r.MistVersion
	case "MISTSERVER_VERSION":
		return r.MistServerVersion
	case "CADDY_VERSION":
		return r.CaddyVersion
	case "CONFIG_SCHEMA_VERSION":
		return r.ConfigSchemaVersion
	case "FRAMEWORKS_CONFIG_SCHEMA_VERSION":
		return r.FrameworksConfigSchemaVersion
	default:
		return ""
	}
}

func boolOr(raw string, fallback bool) bool {
	if parsed, err := strconv.ParseBool(raw); err == nil {
		return parsed
	}
	return fallback
}

func intOr(raw string, fallback int) int {
	if parsed, err := strconv.Atoi(raw); err == nil {
		return parsed
	}
	return fallback
}

func uintOr(raw string) uint64 {
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func floatOr(raw string, fallback float64) float64 {
	if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
		return parsed
	}
	return fallback
}

var installed atomic.Pointer[config.Live[Helmsman]]

// Install makes live the process-wide configuration behind Current, Runtime,
// and the identity accessors, and returns the previously installed one. The
// serve process installs its configuration before starting any component.
func Install(live *config.Live[Helmsman]) *config.Live[Helmsman] {
	return installed.Swap(live)
}

// Current returns the installed configuration snapshot, or nil when none is
// installed.
func Current() *Helmsman {
	if live := installed.Load(); live != nil {
		return live.Get()
	}
	return nil
}

// installedConfig returns the installed configuration. The serve process
// installs it before starting any component, and the subcommands never read
// it, so a read without one is a programming error.
func installedConfig() *Helmsman {
	cfg := Current()
	if cfg == nil {
		panic("appconfig: the Helmsman configuration is read before Install")
	}
	return cfg
}

// Runtime returns the at-use settings of the installed configuration.
func Runtime() EdgeRuntime {
	return installedConfig().EdgeRuntime
}

// NodeID returns NODE_ID from the installed configuration.
func NodeID() string {
	return installedConfig().NodeID
}

// MistServerURL returns MISTSERVER_URL from the installed configuration.
func MistServerURL() string {
	return installedConfig().MistServerURL
}

// MistClient returns the local MistServer API settings: MISTSERVER_URL and the
// MIST_API_USERNAME and MIST_API_PASSWORD credentials. Each call reads the
// installed configuration, so a client built after a SIGHUP env-file reload
// uses the reloaded values.
func MistClient() mist.ClientConfig {
	rt := Runtime()
	return mist.ClientConfig{
		BaseURL:  MistServerURL(),
		Username: rt.MistAPIUsername,
		Password: rt.MistAPIPassword,
	}
}

// EdgePublicURL returns EDGE_PUBLIC_URL from the installed configuration.
func EdgePublicURL() string {
	return installedConfig().EdgePublicURL
}

// StateDir returns HELMSMAN_STATE_DIR from the installed configuration.
func StateDir() string {
	return installedConfig().StateDir
}
