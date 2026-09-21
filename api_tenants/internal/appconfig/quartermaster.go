// Package appconfig holds the typed startup configuration of the binaries in
// this module. scripts/configref generates the operator configuration
// reference from these structs.
package appconfig

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// Quartermaster is the startup configuration of the Quartermaster service.
// The GRPC_TLS_CA_PATH, GRPC_ALLOW_INSECURE, and <SERVICE>_GRPC_TLS_SERVER_NAME
// values used by gRPC health watches are read through a config.Live snapshot
// on every dial, so they follow a SIGHUP env-file reload; every other field
// applies at startup.
//
//configref:service quartermaster cmd=cmd/quartermaster
type Quartermaster struct {
	config.HTTPListen
	config.GRPCListen
	config.HTTPRuntime
	config.Logging
	config.GRPCMetadataPolicy
	config.GeoIP
	config.ServiceAuth
	config.GRPCTLS
	config.Postgres
	config.Registration
	config.DomainEventActor

	Region        string `env:"REGION" desc:"Region label attached to service events sent to Decklog." introduced:"v0.3.0"`
	AdvertiseHost string `env:"QUARTERMASTER_HOST" default:"quartermaster" desc:"Host name this instance advertises when it registers itself." introduced:"v0.3.0"`

	HTTPTLSCertFile string `env:"QUARTERMASTER_HTTP_TLS_CERT_FILE" desc:"Certificate for serving the HTTP listener over TLS. Set together with QUARTERMASTER_HTTP_TLS_KEY_FILE." introduced:"v0.3.0"`
	HTTPTLSKeyFile  string `env:"QUARTERMASTER_HTTP_TLS_KEY_FILE" desc:"Private key for QUARTERMASTER_HTTP_TLS_CERT_FILE." introduced:"v0.3.0"`

	ClusterAccessMaterializationSecret string `env:"CLUSTER_ACCESS_MATERIALIZATION_SECRET" required:"true" secret:"true" desc:"Shared with Purser. Authenticates the commercial and owner grant materialization and revocation envelopes. Read at startup; a rotation takes effect when Purser and Quartermaster restart." introduced:"v0.3.0"`
	ConsentReviewKeyID                 string `env:"CAPACITY_CONSENT_REVIEW_KEY_ID" desc:"Key ID of the Ed25519 key that signs capacity consent reviews. Set together with CAPACITY_CONSENT_REVIEW_PRIVATE_KEY_PEM_B64." introduced:"v0.3.0"`
	ConsentReviewPrivateKeyPEMB64      string `env:"CAPACITY_CONSENT_REVIEW_PRIVATE_KEY_PEM_B64" secret:"true" desc:"Base64-wrapped PKCS#8 Ed25519 private key PEM that signs capacity consent reviews." introduced:"v0.3.0"`

	PlatformRootDomain           string `env:"BRAND_DOMAIN" default:"frameworks.network" desc:"Platform root domain used to build public service host names." introduced:"v0.3.0"`
	PhysicalEndpointStaleSeconds int    `env:"NAVIGATOR_DNS_HEALTH_STALE_SECONDS" default:"300" desc:"Age in seconds after which a physical endpoint health report is too old to publish. Navigator reads the same key so both apply one freshness gate." introduced:"v0.3.0"`

	QuartermasterGRPCAddr string `env:"QUARTERMASTER_GRPC_ADDR" default:"quartermaster:19002" desc:"gRPC address this instance advertises and dials for its own registration." introduced:"v0.3.0"`
	NavigatorGRPCAddr     string `env:"NAVIGATOR_GRPC_ADDR" desc:"Navigator gRPC address. Empty disables DNS features." introduced:"v0.3.0"`
	DecklogGRPCAddr       string `env:"DECKLOG_GRPC_ADDR" default:"decklog:18006" desc:"Decklog gRPC address for service events." introduced:"v0.3.0"`
	PurserGRPCAddr        string `env:"PURSER_GRPC_ADDR" default:"purser:19003" desc:"Purser gRPC address for billing status lookups." introduced:"v0.3.0"`
	CommodoreGRPCAddr     string `env:"COMMODORE_GRPC_ADDR" default:"commodore:19001" desc:"Commodore gRPC address for media authority refresh delivery." introduced:"v0.3.0"`

	QuartermasterGRPCTLSServerName  string `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the self-registration connection and for gRPC health watches of Quartermaster instances. Empty uses the canonical internal name." introduced:"v0.3.0"`
	NavigatorGRPCTLSServerName      string `env:"NAVIGATOR_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Navigator connection and for gRPC health watches of Navigator instances. Empty uses the canonical internal name." introduced:"v0.3.0"`
	DecklogGRPCTLSServerName        string `env:"DECKLOG_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Decklog connection and for gRPC health watches of Decklog instances. Empty uses the canonical internal name." introduced:"v0.3.0"`
	PurserGRPCTLSServerName         string `env:"PURSER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Purser connection and for gRPC health watches of Purser instances. Empty uses the canonical internal name." introduced:"v0.3.0"`
	CommodoreGRPCTLSServerName      string `env:"COMMODORE_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Commodore connection and for gRPC health watches of Commodore instances. Empty uses the canonical internal name." introduced:"v0.3.0"`
	FoghornGRPCTLSServerName        string `env:"FOGHORN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Foghorn instances. Empty uses foghorn.internal." introduced:"v0.3.0"`
	PeriscopeQueryGRPCTLSServerName string `env:"PERISCOPE_QUERY_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Periscope Query instances. Empty uses periscope-query.internal." introduced:"v0.3.0"`
	SignalmanGRPCTLSServerName      string `env:"SIGNALMAN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Signalman instances. Empty uses signalman.internal." introduced:"v0.3.0"`
	DeckhandGRPCTLSServerName       string `env:"DECKHAND_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Deckhand instances. Empty uses deckhand.internal." introduced:"v0.3.0"`
	SkipperGRPCTLSServerName        string `env:"SKIPPER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Skipper instances. Empty uses skipper.internal." introduced:"v0.3.0"`
	LookoutGRPCTLSServerName        string `env:"LOOKOUT_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Lookout instances. Empty uses lookout.internal." introduced:"v0.3.8"`
	BosunGRPCTLSServerName          string `env:"BOSUN_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for gRPC health watches of Bosun instances. Empty uses bosun.internal." introduced:"v0.3.11"`

	HealthPollIntervalSeconds int    `env:"QM_HEALTH_POLL_INTERVAL_SECONDS" default:"30" desc:"Seconds between health poll rounds over registered service instances, varied by up to 25 percent. Must be positive." introduced:"v0.3.0"`
	HealthTimeoutMS           int    `env:"QM_HEALTH_TIMEOUT_MS" default:"2000" desc:"HTTP client timeout in milliseconds for health probes. Each probe is also capped at 2 seconds, and 0 leaves only that cap." introduced:"v0.3.0"`
	HealthMaxConcurrency      int    `env:"QM_HEALTH_MAX_CONCURRENCY" default:"8" desc:"Maximum concurrent HTTP health probes. Zero or negative uses 8." introduced:"v0.3.0"`
	HealthBatchSize           int    `env:"QM_HEALTH_BATCH_SIZE" default:"200" desc:"Maximum service instances probed in one poll round. Zero or negative uses 200." introduced:"v0.3.0"`
	HealthMinAgeSeconds       string `env:"QM_HEALTH_MIN_AGE_SECONDS" desc:"Minimum age in seconds of an instance's last health result before it is probed again. Empty or negative uses QM_HEALTH_POLL_INTERVAL_SECONDS." introduced:"v0.3.0"`
	HealthGRPCWatch           bool   `env:"QM_HEALTH_GRPC_WATCH" default:"true" desc:"Keeps a gRPC health Watch stream open to each registered gRPC instance so status changes are recorded as they happen." introduced:"v0.3.0"`
	HealthWatchRefreshSeconds int    `env:"QM_HEALTH_WATCH_REFRESH_SECONDS" default:"60" desc:"Seconds between refreshes of the set of watched gRPC instances. Must be positive when QM_HEALTH_GRPC_WATCH is true." introduced:"v0.3.0"`
	HealthWatchBackoffSeconds int    `env:"QM_HEALTH_WATCH_BACKOFF_SECONDS" default:"300" desc:"Seconds an instance is skipped after its gRPC health watch cannot be set up or is not implemented." introduced:"v0.3.0"`
	HealthWatchDialTimeoutMS  int    `env:"QM_HEALTH_WATCH_DIAL_TIMEOUT_MS" default:"2000" desc:"Minimum connect timeout in milliseconds for a gRPC health watch connection." introduced:"v0.3.0"`
	HealthWatchMaxConcurrency int    `env:"QM_HEALTH_WATCH_MAX_CONCURRENCY" desc:"Maximum concurrent gRPC health watch streams. Unset, zero, or negative uses QM_HEALTH_MAX_CONCURRENCY." introduced:"v0.3.0"`
}

// HealthMinAge returns QM_HEALTH_MIN_AGE_SECONDS, or -1 when it is unset so
// the health poller falls back to the poll interval.
func (c *Quartermaster) HealthMinAge() int {
	if c.HealthMinAgeSeconds == "" {
		return -1
	}
	seconds, err := strconv.Atoi(c.HealthMinAgeSeconds)
	if err != nil {
		return -1
	}
	return seconds
}

// HealthWatchTLSServerName returns the <SERVICE>_GRPC_TLS_SERVER_NAME override
// for a registered gRPC service type: the declared field whose env key is the
// service ID upper-cased with dashes as underscores. Service types without a
// declared key return empty. Every pkg/servicedefs gRPC service has one
// (TestQuartermasterHealthWatchTLSServerNameCoversEveryGRPCService).
func (c *Quartermaster) HealthWatchTLSServerName(serviceType string) string {
	serviceType = strings.TrimSpace(serviceType)
	if serviceType == "" {
		return ""
	}
	key := strings.ToUpper(strings.ReplaceAll(serviceType, "-", "_")) + "_GRPC_TLS_SERVER_NAME"
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("env") == key && t.Field(i).Type.Kind() == reflect.String {
			return v.Field(i).String()
		}
	}
	return ""
}

// Validate enforces the paired TLS file settings and the health poller
// intervals, which must be positive for the poll loop and watch ticker.
func (c *Quartermaster) Validate() error {
	if (c.HTTPTLSCertFile == "") != (c.HTTPTLSKeyFile == "") {
		return errors.New("QUARTERMASTER_HTTP_TLS_CERT_FILE and QUARTERMASTER_HTTP_TLS_KEY_FILE must be set together")
	}
	if c.HealthPollIntervalSeconds <= 0 {
		return errors.New("QM_HEALTH_POLL_INTERVAL_SECONDS must be positive")
	}
	if c.HealthGRPCWatch && c.HealthWatchRefreshSeconds <= 0 {
		return errors.New("QM_HEALTH_WATCH_REFRESH_SECONDS must be positive when QM_HEALTH_GRPC_WATCH is true")
	}
	if c.HealthMinAgeSeconds != "" {
		if _, err := strconv.Atoi(c.HealthMinAgeSeconds); err != nil {
			return errors.New("QM_HEALTH_MIN_AGE_SECONDS must be an integer")
		}
	}
	return nil
}

// QuartermasterBootstrap is the configuration of `quartermaster bootstrap`
// when it applies or dry-runs a desired state. `--check` reads no
// configuration.
//
//configref:service quartermaster cmd=cmd/quartermaster variant=bootstrap
type QuartermasterBootstrap struct {
	config.Postgres
	config.GeoIP
}
