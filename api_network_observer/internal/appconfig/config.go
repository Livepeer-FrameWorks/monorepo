// Package appconfig defines coordinator and external prober configuration.
package appconfig

import "github.com/Livepeer-FrameWorks/monorepo/pkg/config"

//configref:service network-observer cmd=cmd/network-observer
type Observer struct {
	config.HTTPRuntime
	config.Logging
	config.Postgres
	config.GRPCTLS
	HTTPPort               string   `env:"NETWORK_OBSERVER_PORT" default:"18024" desc:"HTTP port for observer ingest, assignments and candidate feeds." introduced:"v0.3.14"`
	TenantID               string   `env:"NETWORK_OBSERVER_TENANT_ID" required:"true" desc:"Server-owned platform tenant scope for inventory and prober observations." introduced:"v0.3.14"`
	ClusterID              string   `env:"NETWORK_OBSERVER_CLUSTER_ID" required:"true" desc:"Platform cluster identity used for probe telemetry." introduced:"v0.3.14"`
	SharedSecret           string   `env:"NETWORK_OBSERVER_SECRET" required:"true" secret:"true" desc:"Shared bearer secret for trusted external probers and internal candidate readers." introduced:"v0.3.14"`
	ObserverDefinitions    string   `env:"NETWORK_OBSERVERS_JSON" default:"[]" desc:"JSON observer IDs, regions and optional locations; new shared-secret probers can register within the configured limit." introduced:"v0.3.14"`
	MaxObservers           int      `env:"NETWORK_OBSERVER_MAX_PROBERS" default:"32" desc:"Maximum trusted prober identities." introduced:"v0.3.14"`
	Environment            string   `env:"NETWORK_OBSERVER_ENVIRONMENT" default:"production" desc:"Observation environment; staging data remains separate." introduced:"v0.3.14"`
	NetworkID              string   `env:"LIVEPEER_NETWORK_ID" default:"42161" desc:"Expected production Livepeer chain ID, verified against the RPC." introduced:"v0.3.14"`
	EthRPCURL              string   `env:"LIVEPEER_ETH_RPC_URL" required:"true" secret:"true" desc:"Ethereum RPC URL for read-only production catalog discovery." introduced:"v0.3.14"`
	Controller             string   `env:"LIVEPEER_CONTROLLER" required:"true" desc:"Livepeer controller contract used to resolve BondingManager and ServiceRegistry." introduced:"v0.3.14"`
	CatalogIntervalSeconds int      `env:"NETWORK_CATALOG_INTERVAL_SECONDS" default:"900" desc:"Contract catalog refresh interval." introduced:"v0.3.14"`
	DecklogGRPCAddr        string   `env:"DECKLOG_GRPC_ADDR" required:"true" desc:"Decklog telemetry ingest address." introduced:"v0.3.14"`
	ServiceToken           string   `env:"SERVICE_TOKEN" required:"true" secret:"true" desc:"Internal service token for Decklog." introduced:"v0.3.14"`
	KafkaBrokers           []string `env:"KAFKA_BROKERS" required:"true" desc:"Analytics Kafka brokers for gateway discoveries." introduced:"v0.3.14"`
	KafkaClusterID         string   `env:"KAFKA_CLUSTER_ID" default:"local" desc:"Kafka cluster identifier." introduced:"v0.3.14"`
	MirrorRegionPrefixes   []string `env:"MIRROR_REGION_PREFIXES" desc:"MirrorMaker2 source aliases whose analytics topic copies carry gateway discovery from other regions; empty consumes local analytics only." introduced:"v0.3.14"`
	GeoIPPath              string   `env:"GEOIP_MMDB_PATH" desc:"Optional MMDB for endpoint/prober location lookup." introduced:"v0.3.14"`
	TrustedProxyCIDRs      []string `env:"NETWORK_OBSERVER_TRUSTED_PROXY_CIDRS" desc:"Trusted HTTPS ingress source CIDRs; only these peers may forward prober source IPs for automatic geography." introduced:"v0.3.14"`
}

//configref:service network-prober cmd=cmd/network-prober
type Prober struct {
	HealthPort string `env:"NETWORK_PROBER_PORT" default:"18025" desc:"Local prober health and spool statistics HTTP port." introduced:"v0.3.14"`
	config.Logging
	CoordinatorURL           string `env:"NETWORK_OBSERVER_URL" required:"true" desc:"Verified HTTPS observer API, accessible from this external host." introduced:"v0.3.14"`
	SharedSecret             string `env:"NETWORK_OBSERVER_SECRET" required:"true" secret:"true" desc:"Shared observer ingest bearer secret." introduced:"v0.3.14"`
	ObserverID               string `env:"NETWORK_PROBER_ID" required:"true" desc:"Stable prober ID; no CLI enrollment required." introduced:"v0.3.14"`
	Region                   string `env:"NETWORK_PROBER_REGION" required:"true" desc:"Prober vantage region." introduced:"v0.3.14"`
	BroadcasterAddress       string `env:"LIVEPEER_BROADCASTER_ADDRESS" required:"true" desc:"Public broadcaster address for discovery RPC authentication." introduced:"v0.3.14"`
	BroadcasterSignature     string `env:"LIVEPEER_BROADCASTER_SIGNATURE" required:"true" secret:"true" desc:"Hex signature over the broadcaster address using the Livepeer protocol." introduced:"v0.3.14"`
	SkipLivepeerTLSVerify    bool   `env:"LIVEPEER_DISCOVERY_SKIP_TLS_VERIFY" default:"true" desc:"Accept orchestrator self-signed certificates, matching Livepeer discovery; never changes coordinator TLS verification." introduced:"v0.3.14"`
	AllowInsecureCoordinator bool   `env:"NETWORK_PROBER_ALLOW_INSECURE" default:"false" desc:"Allow HTTP coordinator only for isolated local testing." introduced:"v0.3.14"`
	SpoolDirectory           string `env:"NETWORK_PROBER_SPOOL_DIR" required:"true" desc:"Private local durable observation spool directory." introduced:"v0.3.14"`
	SpoolMaxBytes            int    `env:"NETWORK_PROBER_SPOOL_MAX_BYTES" default:"67108864" desc:"Bounded spool byte capacity; overflow is visible in logs and heartbeat reports." introduced:"v0.3.14"`
	Concurrency              int    `env:"NETWORK_PROBER_CONCURRENCY" default:"8" desc:"Maximum parallel DNS/discovery checks." introduced:"v0.3.14"`
	TimeoutSeconds           int    `env:"NETWORK_PROBER_TIMEOUT_SECONDS" default:"10" desc:"Per DNS/RPC operation timeout." introduced:"v0.3.14"`
}
