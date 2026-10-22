// Package appconfig defines coordinator and external prober configuration.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

//configref:service network-observer cmd=cmd/network-observer
type Observer struct {
	config.ListenAddrs
	config.HTTPRuntime
	config.Logging
	config.Postgres
	config.GRPCTLS
	config.Registration
	HTTPPort               string   `env:"NETWORK_OBSERVER_PORT" default:"@port(network-observer)" desc:"HTTP port for observer ingest, assignments and candidate feeds." introduced:"v0.3.13"`
	InternalPort           string   `env:"NETWORK_OBSERVER_INTERNAL_PORT" default:"@port(network-observer.internal)" desc:"HTTPS port serving the same API to mesh callers such as Bridge with the internal-CA certificate in GRPC_TLS_CERT_PATH/GRPC_TLS_KEY_PATH; not opened when those are unset." introduced:"v0.3.13"`
	AdvertiseHost          string   `env:"NETWORK_OBSERVER_HOST" default:"network-observer" desc:"Host name this instance advertises when it registers with Quartermaster." introduced:"v0.3.13"`
	QuartermasterGRPCAddr  string   `env:"QUARTERMASTER_GRPC_ADDR" desc:"Quartermaster gRPC address for service registration, which publishes the observer's mesh DNS name to Bridge. Empty skips registration." introduced:"v0.3.13"`
	QuartermasterTLSName   string   `env:"QUARTERMASTER_GRPC_TLS_SERVER_NAME" desc:"TLS server name override for the Quartermaster connection. Empty uses the canonical internal name." introduced:"v0.3.13"`
	TenantID               string   `env:"NETWORK_OBSERVER_TENANT_ID" required:"true" desc:"Server-owned platform tenant scope for inventory and prober observations." introduced:"v0.3.13"`
	ClusterID              string   `env:"NETWORK_OBSERVER_CLUSTER_ID" required:"true" desc:"Platform cluster identity used for probe telemetry." introduced:"v0.3.13"`
	SharedSecret           string   `env:"NETWORK_OBSERVER_SECRET" required:"true" secret:"true" desc:"Shared bearer secret for trusted external probers and internal candidate readers." introduced:"v0.3.13"`
	ObserverDefinitions    string   `env:"NETWORK_OBSERVERS_JSON" default:"[]" desc:"JSON observer IDs, regions and optional locations; new shared-secret probers can register within the configured limit." introduced:"v0.3.13"`
	MaxObservers           int      `env:"NETWORK_OBSERVER_MAX_PROBERS" default:"32" desc:"Maximum trusted prober identities." introduced:"v0.3.13"`
	Environment            string   `env:"NETWORK_OBSERVER_ENVIRONMENT" default:"production" desc:"Observation environment; staging data remains separate." introduced:"v0.3.13"`
	NetworkID              string   `env:"LIVEPEER_NETWORK_ID" default:"42161" desc:"Expected production Livepeer chain ID, verified against the RPC." introduced:"v0.3.13"`
	EthRPCURL              string   `env:"LIVEPEER_ETH_RPC_URL" required:"true" secret:"true" desc:"Ethereum RPC URL for read-only production catalog discovery." introduced:"v0.3.13"`
	Controller             string   `env:"LIVEPEER_CONTROLLER" required:"true" desc:"Livepeer controller contract used to resolve BondingManager and ServiceRegistry." introduced:"v0.3.13"`
	CatalogIntervalSeconds int      `env:"NETWORK_CATALOG_INTERVAL_SECONDS" default:"900" desc:"Contract catalog refresh interval." introduced:"v0.3.13"`
	DecklogGRPCAddr        string   `env:"DECKLOG_GRPC_ADDR" required:"true" desc:"Decklog telemetry ingest address." introduced:"v0.3.13"`
	ServiceToken           string   `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Internal service token for Decklog." introduced:"v0.3.13"`
	KafkaBrokers           []string `env:"KAFKA_BROKERS" required:"true" desc:"Analytics Kafka brokers for gateway discoveries." introduced:"v0.3.13"`
	KafkaClusterID         string   `env:"KAFKA_CLUSTER_ID" default:"local" desc:"Kafka cluster identifier." introduced:"v0.3.13"`
	MirrorRegionPrefixes   []string `env:"MIRROR_REGION_PREFIXES" desc:"MirrorMaker2 source aliases whose analytics topic copies carry gateway discovery from other regions; empty consumes local analytics only." introduced:"v0.3.13"`
	GeoIPPath              string   `env:"GEOIP_MMDB_PATH" desc:"Optional MMDB for endpoint/prober location lookup." introduced:"v0.3.13"`
	TrustedProxyCIDRs      []string `env:"NETWORK_OBSERVER_TRUSTED_PROXY_CIDRS" desc:"Trusted HTTPS ingress source CIDRs; only these peers may forward prober source IPs for automatic geography." introduced:"v0.3.13"`
}

//configref:service network-prober cmd=cmd/network-prober
type Prober struct {
	HealthPort string `env:"NETWORK_PROBER_PORT" default:"@port(network-prober)" desc:"Local prober health and spool statistics HTTP port." introduced:"v0.3.13"`
	config.ListenAddrs
	config.Logging
	CoordinatorURL           string   `env:"NETWORK_OBSERVER_URL" required:"true" desc:"Verified HTTPS observer relay on the platform's public Bridge API, such as https://bridge.example.net/network-observer; the prober appends the observer's /v1 paths." introduced:"v0.3.13"`
	SharedSecret             string   `env:"NETWORK_OBSERVER_SECRET" required:"true" secret:"true" desc:"Shared observer ingest bearer secret." introduced:"v0.3.13"`
	ObserverID               string   `env:"NETWORK_PROBER_ID" required:"true" desc:"Stable prober ID; no CLI enrollment required." introduced:"v0.3.13"`
	Region                   string   `env:"NETWORK_PROBER_REGION" required:"true" desc:"Prober vantage region." introduced:"v0.3.13"`
	BroadcasterAddress       string   `env:"LIVEPEER_BROADCASTER_ADDRESS" required:"true" desc:"Public broadcaster address for discovery RPC authentication." introduced:"v0.3.13"`
	BroadcasterSignature     string   `env:"LIVEPEER_BROADCASTER_SIGNATURE" required:"true" secret:"true" desc:"Hex signature over the broadcaster address using the Livepeer protocol." introduced:"v0.3.13"`
	SkipLivepeerTLSVerify    bool     `env:"LIVEPEER_DISCOVERY_SKIP_TLS_VERIFY" default:"true" desc:"Accept orchestrator self-signed certificates, matching Livepeer discovery; never changes coordinator TLS verification." introduced:"v0.3.13"`
	AllowInsecureCoordinator bool     `env:"NETWORK_PROBER_ALLOW_INSECURE" default:"false" desc:"Allow HTTP coordinator only for isolated local testing." introduced:"v0.3.13"`
	AllowedPrivateCIDRs      []string `env:"NETWORK_PROBER_ALLOWED_PRIVATE_CIDRS" desc:"Private orchestrator networks the prober may dial; only for isolated local testing, since public discovery never targets them." introduced:"v0.3.13"`
	SpoolDirectory           string   `env:"NETWORK_PROBER_SPOOL_DIR" required:"true" desc:"Private local durable observation spool directory." introduced:"v0.3.13"`
	SpoolMaxBytes            int      `env:"NETWORK_PROBER_SPOOL_MAX_BYTES" default:"67108864" desc:"Bounded spool byte capacity; overflow is visible in logs and heartbeat reports." introduced:"v0.3.13"`
	Concurrency              int      `env:"NETWORK_PROBER_CONCURRENCY" default:"8" desc:"Maximum parallel DNS/discovery checks." introduced:"v0.3.13"`
	TimeoutSeconds           int      `env:"NETWORK_PROBER_TIMEOUT_SECONDS" default:"10" desc:"Per DNS/RPC operation timeout." introduced:"v0.3.13"`
}
