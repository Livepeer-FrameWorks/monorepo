package inventory

import (
	"fmt"
	"slices"
	"strings"
)

// Manifest represents the cluster.yaml configuration
type Manifest struct {
	Version    string   `yaml:"version"`
	Type       string   `yaml:"type"`                  // cluster | edge
	Profile    string   `yaml:"profile,omitempty"`     // control-plane | regional | analytics-only | edge-gateway
	Channel    string   `yaml:"channel,omitempty"`     // release channel: "stable" (default), "rc"
	RootDomain string   `yaml:"root_domain,omitempty"` // Domain for Caddy TLS and routing
	EnvFiles   []string `yaml:"env_files,omitempty"`   // shared env files for all services, merged in order
	HostsFile  string   `yaml:"hosts_file,omitempty"`  // SOPS-encrypted host inventory (IPs + SSH targets)

	// BootstrapOverlay is an optional path (relative to the manifest file) to a GitOps
	// bootstrap overlay (`bootstrap.frameworks.dev/v1alpha1`). Empty = no overlay; the
	// rendered desired-state file uses manifest-derived state only. See
	// docs/architecture/bootstrap-desired-state.md.
	BootstrapOverlay string `yaml:"bootstrap_overlay,omitempty"`

	Hosts          map[string]Host              `yaml:"hosts,omitempty"`
	Clusters       map[string]ClusterConfig     `yaml:"clusters,omitempty"`
	WireGuard      *WireGuardConfig             `yaml:"wireguard,omitempty"`
	Infrastructure InfrastructureConfig         `yaml:"infrastructure,omitempty"`
	Services       map[string]ServiceConfig     `yaml:"services,omitempty"`
	Interfaces     map[string]ServiceConfig     `yaml:"interfaces,omitempty"`
	Observability  map[string]ServiceConfig     `yaml:"observability,omitempty"`
	GeoIP          *GeoIPConfig                 `yaml:"geoip,omitempty"`
	TLSBundles     map[string]TLSBundleConfig   `yaml:"tls_bundles,omitempty"`
	IngressSites   map[string]IngressSiteConfig `yaml:"ingress_sites,omitempty"`
}

func (m *Manifest) SharedEnvFiles() []string {
	if m == nil {
		return nil
	}
	files := make([]string, 0, len(m.EnvFiles))
	for _, file := range m.EnvFiles {
		if strings.TrimSpace(file) == "" {
			continue
		}
		files = append(files, file)
	}
	return files
}

type GeoIPConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Source     string   `yaml:"source,omitempty"`      // maxmind | file
	File       string   `yaml:"file,omitempty"`        // Local MMDB path when source=file
	RemotePath string   `yaml:"remote_path,omitempty"` // Remote MMDB location
	Services   []string `yaml:"services,omitempty"`    // Defaults to foghorn,quartermaster,livepeer-gateway (filtered to those declared in this manifest)
}

type TLSBundleConfig struct {
	Cluster  string            `yaml:"cluster,omitempty"`
	Domains  []string          `yaml:"domains,omitempty"`
	Issuer   string            `yaml:"issuer,omitempty"`
	Email    string            `yaml:"email,omitempty"`
	Metadata map[string]string `yaml:"metadata,omitempty"`
}

type IngressSiteConfig struct {
	Cluster     string            `yaml:"cluster,omitempty"`
	Node        string            `yaml:"node"`
	Domains     []string          `yaml:"domains,omitempty"`
	TLSBundleID string            `yaml:"tls_bundle_id"`
	Kind        string            `yaml:"kind"`
	Upstream    string            `yaml:"upstream"`
	Metadata    map[string]string `yaml:"metadata,omitempty"`
}

// ClusterPricingConfig defines Purser cluster pricing for platform-official clusters.
// When present in the manifest, provision reconciles it authoritatively.
// When absent, existing pricing is left untouched.
type ClusterPricingConfig struct {
	Model             string         `yaml:"model"`                         // free_unmetered, metered, monthly, tier_inherit, custom
	RequiredTierLevel *int           `yaml:"required_tier_level,omitempty"` // 0-5 (0 = no subscription required)
	AllowFreeTier     *bool          `yaml:"allow_free_tier,omitempty"`
	DefaultQuotas     map[string]int `yaml:"default_quotas,omitempty"` // max_streams, max_viewers, max_bandwidth_mbps, retention_days
}

// ClusterConfig defines a cluster to register in Quartermaster during provisioning
type ClusterConfig struct {
	Name             string                `yaml:"name"`
	Type             string                `yaml:"type"` // central, edge
	Region           string                `yaml:"region,omitempty"`
	Roles            []string              `yaml:"roles,omitempty"`             // control, data, analytics, media, mesh, interface, infra, support, observability
	Default          bool                  `yaml:"default,omitempty"`           // is_default_cluster — auto-subscribe new tenants
	PlatformOfficial bool                  `yaml:"platform_official,omitempty"` // is_platform_official — platform-operated cluster
	PublicTopology   bool                  `yaml:"public_topology,omitempty"`   // public map/coverage visibility
	OwnerTenant      string                `yaml:"owner_tenant,omitempty"`      // "frameworks" (system tenant) or bootstrap tenant alias
	Pricing          *ClusterPricingConfig `yaml:"pricing,omitempty"`           // Purser cluster pricing (reconciled authoritatively when present)

	// EnvFiles are env files merged into every cluster-scoped service replica
	// running against this cluster. Merge order is shared Manifest.EnvFiles →
	// ClusterConfig.EnvFiles → ServiceConfig.EnvFile → ServiceConfig.Config →
	// CLI-composed runtime values. Used for per-cluster credentials (e.g.
	// region-specific S3 access keys under the same STORAGE_S3_* names).
	// Relative paths resolve against the manifest directory; absolute paths
	// are rejected. SOPS-encrypted files are decrypted transparently.
	EnvFiles []string `yaml:"env_files,omitempty"`

	// Cell is the regional media cell this cluster belongs to. Multiple
	// clusters can share a cell when ops wants blast-radius isolation inside
	// a region. Defaults to the cluster id when unset (every cluster is its
	// own cell). Maps to quartermaster.infrastructure_clusters.cell_id.
	Cell string `yaml:"cell,omitempty"`

	// Class classifies the cluster for the plan-tier admission filter.
	// Values: platform_official | tenant_private | third_party_marketplace.
	// Empty → derived: PlatformOfficial=true gives "platform_official",
	// otherwise stays empty (operator must set explicitly for marketplace /
	// tenant-private). Maps to cluster_class.
	Class string `yaml:"class,omitempty"`

	// ControlCell names the regional Foghorn cell that owns Helmsman
	// ConfigSeed + tenant alias bundle delivery + edge apply-state ACK for
	// this cluster. Platform-official clusters self-control: defaults to
	// Cell when unset. Tenant-private / marketplace / self-hosted clusters
	// set this explicitly. Maps to control_cell_id.
	ControlCell string `yaml:"control_cell,omitempty"`

	// EligibleServingCells lists the cells permitted to serve this cluster's
	// content. Defaults to [ControlCell] when unset (single-cell). Multi-cell
	// serving is opt-in. Maps to eligible_serving_cell_ids TEXT[].
	EligibleServingCells []string `yaml:"eligible_serving_cells,omitempty"`

	// S3Bucket / S3Endpoint / S3Region carry the artifact storage backend
	// for this cluster. Reconciled into infrastructure_clusters and read by
	// Chandler + cross-cluster federation. Credentials stay env-only via
	// per-cluster env_files; only bucket + endpoint + region live on the
	// cluster row.
	S3Bucket   string `yaml:"s3_bucket,omitempty"`
	S3Endpoint string `yaml:"s3_endpoint,omitempty"`
	S3Region   string `yaml:"s3_region,omitempty"`

	// AllowPrivatePullSources opts the cluster's pull-source validator in to
	// RFC1918/multicast literals. Default false (strict) so platform-official
	// clusters reject tenant-private upstreams. Self-hosted clusters set this
	// to true when their edges legitimately pull from LAN/VPC origins.
	// Translated to FRAMEWORKS_ALLOW_PRIVATE_PULL_SOURCES on commodore + foghorn
	// service envs by cluster_provision.
	AllowPrivatePullSources bool `yaml:"allow_private_pull_sources,omitempty"`
}

// Host represents a target machine
type Host struct {
	Name       string            `yaml:"-"` // Populated from the Hosts map key, not from YAML
	ExternalIP string            `yaml:"external_ip"`
	User       string            `yaml:"user"`
	Cluster    string            `yaml:"cluster,omitempty"` // Explicit cluster membership
	Roles      []string          `yaml:"roles,omitempty"`
	Labels     map[string]string `yaml:"labels,omitempty"`

	// WireGuard mesh identity. WireguardIP and WireguardPublicKey are written
	// to the plaintext cluster manifest by `frameworks mesh wg generate`;
	// WireguardPrivateKey is merged in from the SOPS-encrypted host inventory.
	WireguardIP         string `yaml:"wireguard_ip,omitempty"`
	WireguardPublicKey  string `yaml:"wireguard_public_key,omitempty"`
	WireguardPort       int    `yaml:"wireguard_port,omitempty"`
	WireguardPrivateKey string `yaml:"-"`

	// Adopted-local nodes carry the private key on their own disk rather
	// than in SOPS. `mesh reconcile --write-gitops` sets Managed=false and
	// records the on-disk path; the Ansible role uses this to preserve the
	// existing key instead of rendering a new one.
	WireguardPrivateKeyFile    string `yaml:"wireguard_private_key_file,omitempty"`
	WireguardPrivateKeyManaged *bool  `yaml:"wireguard_private_key_managed,omitempty"`
}

// WireGuardConfig represents WireGuard mesh configuration
type WireGuardConfig struct {
	Enabled         bool            `yaml:"enabled"`
	Interface       string          `yaml:"interface,omitempty"`
	ManageHostsFile bool            `yaml:"manage_hosts_file,omitempty"`
	Peers           []WireGuardPeer `yaml:"peers,omitempty"`

	MeshCIDR   string `yaml:"mesh_cidr,omitempty"`
	ListenPort int    `yaml:"listen_port,omitempty"`
}

// WireGuardPeer represents a peer in the WireGuard mesh
type WireGuardPeer struct {
	Name       string   `yaml:"name"`
	PublicKey  string   `yaml:"public_key"`
	Endpoint   string   `yaml:"endpoint,omitempty"`
	AllowedIPs []string `yaml:"allowed_ips,omitempty"`
}

// InfrastructureConfig represents infrastructure services (native installs)
type InfrastructureConfig struct {
	Postgres   *PostgresConfig   `yaml:"postgres,omitempty"`
	Redis      *RedisConfig      `yaml:"redis,omitempty"`
	Kafka      *KafkaConfig      `yaml:"kafka,omitempty"`
	ClickHouse *ClickHouseConfig `yaml:"clickhouse,omitempty"`
}

// PostgresConfig represents Postgres/YugabyteDB configuration
type PostgresConfig struct {
	Enabled           bool               `yaml:"enabled"`
	Engine            string             `yaml:"engine,omitempty"` // "postgres" (default) or "yugabyte"
	Mode              string             `yaml:"mode"`             // native (only supported mode for infrastructure)
	Version           string             `yaml:"version"`
	Host              string             `yaml:"host,omitempty"`  // Single-host (vanilla Postgres)
	Nodes             []PostgresNode     `yaml:"nodes,omitempty"` // Multi-node (YugabyteDB)
	Instances         []PostgresInstance `yaml:"instances,omitempty"`
	Port              int                `yaml:"port"`
	ReplicationFactor int                `yaml:"replication_factor,omitempty"` // Default: len(Nodes)
	Databases         []DatabaseConfig   `yaml:"databases,omitempty"`
	Tuning            map[string]string  `yaml:"tuning,omitempty"`
	SQLAccess         string             `yaml:"sql_access,omitempty"` // "direct" (default) or "ssh"
	Password          string             `yaml:"password,omitempty"`
}

// PostgresInstance represents an additional named vanilla PostgreSQL instance.
type PostgresInstance struct {
	Name      string            `yaml:"name"`
	Host      string            `yaml:"host"`
	Port      int               `yaml:"port,omitempty"`
	Version   string            `yaml:"version,omitempty"`
	Mode      string            `yaml:"mode,omitempty"`
	Password  string            `yaml:"password,omitempty"`
	Databases []DatabaseConfig  `yaml:"databases,omitempty"`
	Tuning    map[string]string `yaml:"tuning,omitempty"`
	Config    map[string]string `yaml:"config,omitempty"`
}

// PostgresNode represents a node in a multi-node Postgres/YugabyteDB cluster
type PostgresNode struct {
	Host    string `yaml:"host"`               // Host name from Hosts map
	ID      int    `yaml:"id"`                 // Node ID (1-based)
	RpcPort int    `yaml:"rpc_port,omitempty"` // yb-master RPC (default 7100)
}

// IsYugabyte returns true if this config uses YugabyteDB engine
func (pg *PostgresConfig) IsYugabyte() bool {
	return pg.Engine == "yugabyte"
}

// EffectivePort returns the configured port or the engine default (5433 for YB, 5432 for PG)
func (pg *PostgresConfig) EffectivePort() int {
	if pg.Port != 0 {
		return pg.Port
	}
	if pg.IsYugabyte() {
		return 5433
	}
	return 5432
}

// AllHosts returns all host names for this config (single Host or multi-node Nodes)
func (pg *PostgresConfig) AllHosts() []string {
	hosts := make([]string, 0, len(pg.Nodes)+len(pg.Instances)+1)
	if len(pg.Nodes) > 0 {
		for _, n := range pg.Nodes {
			hosts = append(hosts, n.Host)
		}
	} else if pg.Host != "" {
		hosts = append(hosts, pg.Host)
	}
	for _, inst := range pg.Instances {
		if inst.Host != "" {
			hosts = append(hosts, inst.Host)
		}
	}
	return hosts
}

// MasterAddresses builds the comma-separated master addresses string for YugabyteDB.
// resolve returns the peer address for a host name — typically manifest.MeshAddress.
func (pg *PostgresConfig) MasterAddresses(resolve func(hostName string) string) string {
	if len(pg.Nodes) == 0 {
		return ""
	}
	addrs := make([]string, 0, len(pg.Nodes))
	for _, node := range pg.Nodes {
		rpcPort := node.RpcPort
		if rpcPort == 0 {
			rpcPort = 7100
		}
		addrs = append(addrs, fmt.Sprintf("%s:%d", resolve(node.Host), rpcPort))
	}
	return strings.Join(addrs, ",")
}

// EffectiveReplicationFactor returns the replication factor, defaulting to len(Nodes)
func (pg *PostgresConfig) EffectiveReplicationFactor() int {
	if pg.ReplicationFactor > 0 {
		return pg.ReplicationFactor
	}
	if len(pg.Nodes) > 0 {
		return len(pg.Nodes)
	}
	return 1
}

// DatabaseConfig represents a Postgres database
type DatabaseConfig struct {
	Name  string `yaml:"name"`
	Owner string `yaml:"owner"`
}

// KafkaConfig declares a KRaft-mode Kafka cluster. The top-level fields
// describe the primary cluster; Regional carries any additional clusters in
// other regions. Non-empty Controllers selects dedicated controller mode;
// empty Controllers selects combined controller+broker mode on each broker
// node.
type KafkaConfig struct {
	Enabled                              bool              `yaml:"enabled"`
	Mode                                 string            `yaml:"mode"` // native
	Version                              string            `yaml:"version"`
	ClusterID                            string            `yaml:"cluster_id"`                // KRaft cluster UUID (required)
	RegionID                             string            `yaml:"region_id,omitempty"`       // e.g. "eu-west". Empty = infer from broker host labels.
	Role                                 string            `yaml:"role,omitempty"`            // "aggregator" | "regional". Empty top-level = aggregator.
	ControllerPort                       int               `yaml:"controller_port,omitempty"` // Combined mode: controller port (default 9093)
	Controllers                          []KafkaController `yaml:"controllers,omitempty"`     // Dedicated controllers (if absent → combined mode)
	Brokers                              []KafkaBroker     `yaml:"brokers,omitempty"`
	Topics                               []KafkaTopic      `yaml:"topics,omitempty"`
	DeleteTopicEnable                    *bool             `yaml:"delete_topic_enable,omitempty"`
	MinInSyncReplicas                    int               `yaml:"min_insync_replicas,omitempty"`
	OffsetsTopicReplicationFactor        int               `yaml:"offsets_topic_replication_factor,omitempty"`
	TransactionStateLogReplicationFactor int               `yaml:"transaction_state_log_replication_factor,omitempty"`
	TransactionStateLogMinISR            int               `yaml:"transaction_state_log_min_isr,omitempty"`

	// Regional declares additional Kafka clusters in other regions, keyed
	// by region_id. Each entry is an independent KRaft deployment. Role
	// marks which cluster aggregates mirrored topics; empty on a regional
	// entry means "regional". MirrorMaker2 mirrors topics between regional
	// and aggregator clusters.
	Regional []RegionalKafkaCluster `yaml:"regional,omitempty"`

	// MirrorMaker declares the dedicated MM2 workers that mirror
	// RegionalKafkaCluster entries (Role="regional") into the aggregator
	// cluster. When absent or disabled, no mirroring is provisioned regardless
	// of Regional declarations.
	MirrorMaker *KafkaMirrorMakerConfig `yaml:"mirrormaker,omitempty"`
}

// KafkaMirrorMakerConfig declares the hosts running the dedicated MM2 workers.
// Source clusters are derived from KafkaConfig.Regional with Role!="aggregator"
// (or empty Role). The aggregator target is the first Role="aggregator" entry,
// or the primary KafkaConfig when none is marked.
type KafkaMirrorMakerConfig struct {
	Enabled   bool     `yaml:"enabled"`
	Mode      string   `yaml:"mode,omitempty"`      // native (default; same Kafka tarball)
	Host      string   `yaml:"host,omitempty"`      // Optional single worker host; prefer Hosts.
	Hosts     []string `yaml:"hosts,omitempty"`     // Hosts running the dedicated MM2 worker cluster.
	HeapOpts  string   `yaml:"heap_opts,omitempty"` // JVM heap (default -Xmx1G -Xms1G)
	Replicas  int      `yaml:"replicas,omitempty"`  // Source-cluster replication factor for mirrored topics; default 1
	TaskCount int      `yaml:"task_count,omitempty"`
}

// RegionalKafkaCluster is an additional Kafka cluster pinned to a region.
// Each cluster carries its own KRaft ID, controller/broker hosts, and
// topic list. When Role="regional", topics in MirrorTopics are mirrored
// to the cluster whose Role="aggregator" (or the primary KafkaConfig if
// no aggregator is declared); aggregator-side topic names are prefixed
// with "{region_id}." per MM2 default.
type RegionalKafkaCluster struct {
	RegionID                             string            `yaml:"region_id"`      // e.g. "us-east"
	Role                                 string            `yaml:"role,omitempty"` // "regional" (default) | "aggregator"
	ClusterID                            string            `yaml:"cluster_id"`     // KRaft cluster UUID (required)
	ControllerPort                       int               `yaml:"controller_port,omitempty"`
	Controllers                          []KafkaController `yaml:"controllers,omitempty"`
	Brokers                              []KafkaBroker     `yaml:"brokers,omitempty"`
	Topics                               []KafkaTopic      `yaml:"topics,omitempty"`
	DeleteTopicEnable                    *bool             `yaml:"delete_topic_enable,omitempty"`
	MinInSyncReplicas                    int               `yaml:"min_insync_replicas,omitempty"`
	OffsetsTopicReplicationFactor        int               `yaml:"offsets_topic_replication_factor,omitempty"`
	TransactionStateLogReplicationFactor int               `yaml:"transaction_state_log_replication_factor,omitempty"`
	TransactionStateLogMinISR            int               `yaml:"transaction_state_log_min_isr,omitempty"`
	// MirrorTopics names the topics MirrorMaker2 mirrors from this
	// regional cluster into the aggregator. Empty = mirror the canonical
	// set: analytics_events, service_events, billing.usage_reports,
	// decklog_events_dlq. Other topics stay regional-only.
	MirrorTopics []string `yaml:"mirror_topics,omitempty"`
}

// KafkaController represents a dedicated KRaft controller node.
type KafkaController struct {
	Host  string `yaml:"host"`   // Host name from Hosts map
	ID    int    `yaml:"id"`     // Controller node ID (must not overlap with broker IDs)
	Port  int    `yaml:"port"`   // Controller listener port (default 9093)
	DirID string `yaml:"dir_id"` // Directory UUID for dynamic quorum bootstrap (generate with kafka-storage.sh random-uuid)
}

// KafkaBroker represents a Kafka broker
type KafkaBroker struct {
	Host string `yaml:"host"` // Host name from Hosts map
	ID   int    `yaml:"id"`
	Port int    `yaml:"port"`
}

// KafkaTopic represents a Kafka topic configuration
type KafkaTopic struct {
	Name              string            `yaml:"name"`
	Partitions        int               `yaml:"partitions"`
	ReplicationFactor int               `yaml:"replication_factor"`
	Config            map[string]string `yaml:"config,omitempty"`
}

// ClickHouseConfig represents ClickHouse configuration. ClickHouse is always a
// Replicated cluster (N>=1): a single node is a one-element Nodes list that still
// gets Keeper + the Replicated schema. There is no non-replicated singleton mode.
type ClickHouseConfig struct {
	Enabled bool             `yaml:"enabled"`
	Mode    string           `yaml:"mode"` // native
	Version string           `yaml:"version"`
	Nodes   []ClickHouseNode `yaml:"nodes,omitempty"` // Replicated cluster nodes (>=1); sole PROVISIONING target
	// ReadEndpoint / WriteEndpoint decouple the SERVICE endpoint from the
	// provisioning target (Nodes). They are host keys: ReadEndpoint overrides
	// periscope-query's CLICKHOUSE_ADDR, WriteEndpoint overrides periscope-ingest's.
	// Empty → use Nodes. Used during migration to keep services on the live old node
	// while the new node is provisioned, then flip write-then-read at cutover (write
	// first so ingest drains into the new node, read after so dashboards never read
	// a node mid-drain).
	ReadEndpoint  string `yaml:"read_endpoint,omitempty"`
	WriteEndpoint string `yaml:"write_endpoint,omitempty"`
	// Host is a removed field, retained only as a tombstone so an old `host:`
	// manifest fails validation with a clear migration message instead of a
	// strict-unmarshal "unknown field" error. Never read outside parser validation.
	Host      string   `yaml:"host,omitempty"`
	Port      int      `yaml:"port"`
	Databases []string `yaml:"databases,omitempty"`
	SQLAccess string   `yaml:"sql_access,omitempty"` // "direct" (default) or "ssh"
}

// EndpointFor returns the host-key override a given periscope service should use
// for CLICKHOUSE_ADDR, or "" to fall back to Nodes. periscope-query follows
// ReadEndpoint; periscope-ingest follows WriteEndpoint.
func (ch *ClickHouseConfig) EndpointFor(serviceID string) string {
	switch serviceID {
	case "periscope-query":
		return ch.ReadEndpoint
	case "periscope-ingest":
		return ch.WriteEndpoint
	}
	return ""
}

// ClickHouseNode represents a node in a Replicated ClickHouse cluster.
// No per-node port: every node uses the cluster-level ClickHouseConfig.Port.
type ClickHouseNode struct {
	Host string `yaml:"host"` // Host name from Hosts map
	ID   int    `yaml:"id"`   // Node ID (>=1); drives task names, Keeper server_id, {replica}
}

// EffectivePort returns the configured native port or the default 9000.
func (ch *ClickHouseConfig) EffectivePort() int {
	if ch.Port != 0 {
		return ch.Port
	}
	return 9000
}

// CoordinatorHost returns the host of the node with the lowest positive ID — the
// deterministic, reorder-proof coordinator for single-writer operations (table
// DDL, migrations, seed, backup/snapshot/restore). NOT the YAML-order first node.
func (ch *ClickHouseConfig) CoordinatorHost() string {
	bestID, bestHost := 0, ""
	for _, n := range ch.Nodes {
		if n.ID <= 0 {
			continue
		}
		if bestID == 0 || n.ID < bestID {
			bestID, bestHost = n.ID, n.Host
		}
	}
	return bestHost
}

// HasHost reports whether name is one of this config's hosts.
func (ch *ClickHouseConfig) HasHost(name string) bool {
	return slices.Contains(ch.AllHosts(), name)
}

// IsMultiNode reports whether the manifest exceeds the supported single-node
// ClickHouse bootstrap shape.
func (ch *ClickHouseConfig) IsMultiNode() bool {
	return len(ch.Nodes) > 1
}

// AllHosts returns every node host name for this Replicated cluster.
func (ch *ClickHouseConfig) AllHosts() []string {
	hosts := make([]string, 0, len(ch.Nodes))
	for _, n := range ch.Nodes {
		hosts = append(hosts, n.Host)
	}
	return hosts
}

// AllAddrs returns host:port for every node, applying the cluster-level port.
// resolve maps a host name to its reachable address (typically manifest.MeshAddress
// or a mesh hostname); pass nil to use raw host names.
func (ch *ClickHouseConfig) AllAddrs(resolve func(hostName string) string) []string {
	hosts := ch.AllHosts()
	addrs := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if resolve != nil {
			h = resolve(h)
		}
		if h == "" {
			continue
		}
		addrs = append(addrs, fmt.Sprintf("%s:%d", h, ch.EffectivePort()))
	}
	return addrs
}

// RedisConfig represents Redis instance configuration
type RedisConfig struct {
	Enabled   bool            `yaml:"enabled"`
	Engine    string          `yaml:"engine,omitempty"`  // "valkey" (default) or "redis"
	Mode      string          `yaml:"mode"`              // "docker" or "native"
	Version   string          `yaml:"version,omitempty"` // e.g., "8.1" or "7.2.4"
	Instances []RedisInstance `yaml:"instances"`
}

// RedisInstance represents a single named Redis instance
type RedisInstance struct {
	Name         string              `yaml:"name"`                    // e.g., "foghorn", "platform"
	Engine       string              `yaml:"engine,omitempty"`        // Optional override: "valkey" or "redis"
	Mode         string              `yaml:"mode,omitempty"`          // "single" (default) | "sentinel" — Sentinel signals HA with replica nodes + sentinel quorum
	Host         string              `yaml:"host"`                    // Primary host (mode=single) or initial master (mode=sentinel)
	Port         int                 `yaml:"port"`                    // Default: 6379
	Password     string              `yaml:"password,omitempty"`      // AUTH password
	Cluster      string              `yaml:"cluster,omitempty"`       // Scope: only consumers in this cluster see this instance under its Name; empty = applies globally
	MasterName   string              `yaml:"master_name,omitempty"`   // Sentinel master name; defaults to Name when empty
	ReplicaHosts []string            `yaml:"replica_hosts,omitempty"` // Sentinel replica node hosts (excludes Host)
	Sentinels    []RedisSentinelNode `yaml:"sentinels,omitempty"`     // Sentinel quorum members; consumers connect through these
	Config       map[string]string   `yaml:"config,omitempty"`        // maxmemory, appendonly, etc.
}

// RedisSentinelNode is one member of a Sentinel quorum. Sentinel uses 26379
// by default; the operator must run an odd-count quorum (3 or 5).
type RedisSentinelNode struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port,omitempty"` // Default: 26379
}

// ServiceConfig represents a FrameWorks application or interface service
type ServiceConfig struct {
	Enabled        bool                  `yaml:"enabled"`
	Mode           string                `yaml:"mode"` // docker | native
	Version        string                `yaml:"version"`
	Image          string                `yaml:"image,omitempty"`      // For docker mode
	BinaryURL      string                `yaml:"binary_url,omitempty"` // For native mode
	Deploy         string                `yaml:"deploy,omitempty"`     // Underlying service slug (container/binary name)
	Cluster        string                `yaml:"cluster,omitempty"`    // Explicit cluster assignment (singular shorthand for Clusters[0])
	Clusters       []string              `yaml:"clusters,omitempty"`   // Logical cluster assignments for cluster-scoped media services (M:N)
	Host           string                `yaml:"host,omitempty"`       // Single host
	Hosts          []string              `yaml:"hosts,omitempty"`      // Multiple hosts (for replicas)
	Port           int                   `yaml:"port,omitempty"`
	GRPCPort       int                   `yaml:"grpc_port,omitempty"`
	Replicas       int                   `yaml:"replicas,omitempty"`
	EnvFile        string                `yaml:"env_file,omitempty"`
	DependsOn      []string              `yaml:"depends_on,omitempty"`
	Public         bool                  `yaml:"public,omitempty"`          // Has public-facing endpoint
	Config         map[string]string     `yaml:"config,omitempty"`          // Service-specific config
	UpdateStrategy *UpdateStrategyConfig `yaml:"update_strategy,omitempty"` // Optional cluster apply rollout override
}

type UpdateStrategyConfig struct {
	MaxUnavailable *int  `yaml:"max_unavailable,omitempty"`
	Canary         *int  `yaml:"canary,omitempty"`
	RegionStagger  *bool `yaml:"region_stagger,omitempty"`
	PrimaryLast    *bool `yaml:"primary_last,omitempty"`
}

// EdgeManifest represents edge node deployment configuration (edges.yaml)
type EdgeManifest struct {
	Version         string     `yaml:"version"`
	Type            string     `yaml:"type,omitempty"`    // edge; accepted for consistency with cluster manifests
	Channel         string     `yaml:"channel,omitempty"` // Release channel: "stable", "rc", or explicit version (e.g., "v0.2.0-rc3")
	RootDomain      string     `yaml:"root_domain"`
	PoolDomain      string     `yaml:"pool_domain"` // Shared edge pool domain (e.g., edge.media-eu.example.com)
	Email           string     `yaml:"email"`       // ACME email
	ClusterID       string     `yaml:"cluster_id,omitempty"`
	ClusterManifest string     `yaml:"cluster_manifest,omitempty"` // Cluster manifest used to resolve platform SERVICE_TOKEN for register_qm
	EnrollmentToken string     `yaml:"enrollment_token,omitempty"` // Token for node bootstrap
	HostsFile       string     `yaml:"hosts_file,omitempty"`       // SOPS-encrypted host inventory
	FetchCert       bool       `yaml:"fetch_cert,omitempty"`       // Deprecated for manifest provisioning; edge TLS is delivered by ConfigSeed
	Mode            string     `yaml:"mode,omitempty"`             // "container" (default; single edge image) or "native" ("docker" = deprecated alias)
	Capabilities    []string   `yaml:"capabilities,omitempty"`     // Default node capabilities: ingest, edge, storage, processing
	BandwidthMbps   int        `yaml:"bandwidth_mbps,omitempty"`   // Default egress bandwidth limit advertised by Helmsman
	MaxTranscodes   int        `yaml:"max_transcodes,omitempty"`   // Default local transcoding concurrency limit
	StorageBytes    uint64     `yaml:"storage_capacity_bytes,omitempty"`
	Nodes           []EdgeNode `yaml:"nodes"`
}

// EdgeNode represents a single edge node in the manifest
type EdgeNode struct {
	Name          string            `yaml:"name"`                   // Unique node name (e.g., edge-us-east-1)
	SSH           string            `yaml:"ssh"`                    // SSH target (user@host); composed by MergeEdgeHosts when loading via inventory
	SSHKey        string            `yaml:"-"`                      // Populated from --ssh-key flag, not from YAML
	Subdomain     string            `yaml:"subdomain,omitempty"`    // Individual subdomain (e.g., edge-us-east-1 -> edge-us-east-1.example.com)
	Region        string            `yaml:"region,omitempty"`       // Region for registration
	Cluster       string            `yaml:"cluster,omitempty"`      // Per-node cluster override; falls back to EdgeManifest.ClusterID when unset. Needed when one edge manifest registers nodes across multiple clusters (e.g. edge-eu-1 → media-eu-1, edge-us-1 → media-us-1).
	Labels        map[string]string `yaml:"labels,omitempty"`       // Additional labels
	ApplyTune     bool              `yaml:"apply_tune,omitempty"`   // Apply sysctl tuning
	RegisterQM    bool              `yaml:"register_qm,omitempty"`  // Register in Quartermaster
	Mode          string            `yaml:"mode,omitempty"`         // Per-node mode override ("container"|"native"; "docker" = deprecated alias)
	Capabilities  []string          `yaml:"capabilities,omitempty"` // Per-node capability override
	BandwidthMbps int               `yaml:"bandwidth_mbps,omitempty"`
	MaxTranscodes int               `yaml:"max_transcodes,omitempty"`
	StorageBytes  uint64            `yaml:"storage_capacity_bytes,omitempty"`
	ExternalIP    string            `yaml:"-"` // Populated by MergeEdgeHosts from the inventory; lets registration skip the remote ifconfig.me probe.
}

// ResolvedCluster returns the cluster ID this edge node should register
// against. Per-node Cluster wins over the EdgeManifest's top-level
// ClusterID, so a single edges.yaml can spread nodes across multiple
// clusters.
func (n EdgeNode) ResolvedCluster(manifestClusterID string) string {
	if n.Cluster != "" {
		return n.Cluster
	}
	return manifestClusterID
}

func (n EdgeNode) ResolvedCapabilities(defaults []string) []string {
	if len(n.Capabilities) > 0 {
		return append([]string(nil), n.Capabilities...)
	}
	return append([]string(nil), defaults...)
}

func (n EdgeNode) ResolvedBandwidthMbps(defaultValue int) int {
	if n.BandwidthMbps > 0 {
		return n.BandwidthMbps
	}
	return defaultValue
}

func (n EdgeNode) ResolvedMaxTranscodes(defaultValue int) int {
	if n.MaxTranscodes > 0 {
		return n.MaxTranscodes
	}
	return defaultValue
}

func (n EdgeNode) ResolvedStorageBytes(defaultValue uint64) uint64 {
	if n.StorageBytes > 0 {
		return n.StorageBytes
	}
	return defaultValue
}

// ResolvedChannel returns the effective release channel, defaulting to "stable".
func (m *Manifest) ResolvedChannel() string {
	if m.Channel != "" {
		return m.Channel
	}
	return "stable"
}

// HostInventory holds connection details loaded from a SOPS-encrypted hosts file.
type HostInventory struct {
	Hosts     map[string]HostConnection `yaml:"hosts"`
	EdgeNodes map[string]EdgeConnection `yaml:"edge_nodes,omitempty"`
}

// HostConnection holds the sensitive connection details for a cluster host.
type HostConnection struct {
	ExternalIP          string `yaml:"external_ip"`
	User                string `yaml:"user,omitempty"`
	WireguardPrivateKey string `yaml:"wireguard_private_key,omitempty"`

	// Adopted-local hosts keep their WireGuard private key on-disk rather
	// than in SOPS. `mesh reconcile --write-gitops` sets Managed=false and
	// records the on-disk path; the Privateer Ansible role uses this to
	// preserve the key file instead of rendering a new one.
	WireguardPrivateKeyFile    string `yaml:"wireguard_private_key_file,omitempty"`
	WireguardPrivateKeyManaged *bool  `yaml:"wireguard_private_key_managed,omitempty"`
}

// EdgeConnection holds the sensitive connection details for an edge node.
// Mirrors HostConnection so operators see the same shape under hosts: and
// edge_nodes: in the same encrypted file. Edges don't join the WireGuard mesh,
// so no key fields here.
type EdgeConnection struct {
	ExternalIP string `yaml:"external_ip"`
	User       string `yaml:"user,omitempty"`
}

// ResolvedMode returns the effective mode for this node, falling back to the
// manifest default, then to "container" (the single edge image).
func (n EdgeNode) ResolvedMode(manifestDefault string) string {
	if n.Mode != "" {
		return n.Mode
	}
	if manifestDefault != "" {
		return manifestDefault
	}
	return "container"
}
