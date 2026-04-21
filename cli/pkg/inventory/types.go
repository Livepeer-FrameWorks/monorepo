package inventory

import (
	"fmt"
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
	Services   []string `yaml:"services,omitempty"`    // Defaults to foghorn,quartermaster
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
	OwnerTenant      string                `yaml:"owner_tenant,omitempty"`      // "frameworks" (system tenant) or tenant UUID
	Pricing          *ClusterPricingConfig `yaml:"pricing,omitempty"`           // Purser cluster pricing (reconciled authoritatively when present)
}

// Host represents a target machine
type Host struct {
	Name       string            `yaml:"-"` // Populated from the Hosts map key, not from YAML
	ExternalIP string            `yaml:"external_ip"`
	User       string            `yaml:"user"`
	Cluster    string            `yaml:"cluster,omitempty"` // Explicit cluster membership
	Roles      []string          `yaml:"roles,omitempty"`
	Labels     map[string]string `yaml:"labels,omitempty"`
}

// WireGuardConfig represents WireGuard mesh configuration
type WireGuardConfig struct {
	Enabled         bool            `yaml:"enabled"`
	Interface       string          `yaml:"interface,omitempty"`
	ManageHostsFile bool            `yaml:"manage_hosts_file,omitempty"`
	Peers           []WireGuardPeer `yaml:"peers,omitempty"`
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
	Zookeeper  *ZookeeperConfig  `yaml:"zookeeper,omitempty"`
	Kafka      *KafkaConfig      `yaml:"kafka,omitempty"`
	ClickHouse *ClickHouseConfig `yaml:"clickhouse,omitempty"`
}

// PostgresConfig represents Postgres/YugabyteDB configuration
type PostgresConfig struct {
	Enabled           bool              `yaml:"enabled"`
	Engine            string            `yaml:"engine,omitempty"` // "postgres" (default) or "yugabyte"
	Mode              string            `yaml:"mode"`             // native (only supported mode for infrastructure)
	Version           string            `yaml:"version"`
	Host              string            `yaml:"host,omitempty"`  // Single-host (vanilla Postgres)
	Nodes             []PostgresNode    `yaml:"nodes,omitempty"` // Multi-node (YugabyteDB)
	Port              int               `yaml:"port"`
	ReplicationFactor int               `yaml:"replication_factor,omitempty"` // Default: len(Nodes)
	Databases         []DatabaseConfig  `yaml:"databases,omitempty"`
	Tuning            map[string]string `yaml:"tuning,omitempty"`
	SQLAccess         string            `yaml:"sql_access,omitempty"` // "direct" (default) or "ssh"
	Password          string            `yaml:"password,omitempty"`
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
	if len(pg.Nodes) > 0 {
		hosts := make([]string, len(pg.Nodes))
		for i, n := range pg.Nodes {
			hosts[i] = n.Host
		}
		return hosts
	}
	if pg.Host != "" {
		return []string{pg.Host}
	}
	return nil
}

// MasterAddresses builds the comma-separated master addresses string for YugabyteDB.
// Resolves host names to IPs using the provided manifest hosts map.
func (pg *PostgresConfig) MasterAddresses(hosts map[string]Host) string {
	if len(pg.Nodes) == 0 {
		return ""
	}
	addrs := make([]string, 0, len(pg.Nodes))
	for _, node := range pg.Nodes {
		rpcPort := node.RpcPort
		if rpcPort == 0 {
			rpcPort = 7100
		}
		h, ok := hosts[node.Host]
		if !ok {
			continue
		}
		addrs = append(addrs, fmt.Sprintf("%s:%d", h.ExternalIP, rpcPort))
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

// ZookeeperConfig represents Zookeeper ensemble configuration
type ZookeeperConfig struct {
	Enabled  bool            `yaml:"enabled"`
	Mode     string          `yaml:"mode"` // native
	Version  string          `yaml:"version"`
	Ensemble []ZookeeperNode `yaml:"ensemble,omitempty"`
}

// ZookeeperNode represents a single Zookeeper node
type ZookeeperNode struct {
	Host string `yaml:"host"` // Host name from Hosts map
	ID   int    `yaml:"id"`
	Port int    `yaml:"port"`
}

// KafkaConfig represents Kafka cluster configuration (KRaft-only, no ZooKeeper).
// If Controllers is non-empty, dedicated controller mode is used (separate controller + broker processes).
// If Controllers is empty, combined broker+controller mode is used on each broker node.
type KafkaConfig struct {
	Enabled                              bool              `yaml:"enabled"`
	Mode                                 string            `yaml:"mode"` // native
	Version                              string            `yaml:"version"`
	ClusterID                            string            `yaml:"cluster_id"`                // KRaft cluster UUID (required)
	ControllerPort                       int               `yaml:"controller_port,omitempty"` // Combined mode: controller port (default 9093)
	Controllers                          []KafkaController `yaml:"controllers,omitempty"`     // Dedicated controllers (if absent → combined mode)
	Brokers                              []KafkaBroker     `yaml:"brokers,omitempty"`
	Topics                               []KafkaTopic      `yaml:"topics,omitempty"`
	DeleteTopicEnable                    *bool             `yaml:"delete_topic_enable,omitempty"`
	MinInSyncReplicas                    int               `yaml:"min_insync_replicas,omitempty"`
	OffsetsTopicReplicationFactor        int               `yaml:"offsets_topic_replication_factor,omitempty"`
	TransactionStateLogReplicationFactor int               `yaml:"transaction_state_log_replication_factor,omitempty"`
	TransactionStateLogMinISR            int               `yaml:"transaction_state_log_min_isr,omitempty"`
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

// ClickHouseConfig represents ClickHouse configuration
type ClickHouseConfig struct {
	Enabled   bool     `yaml:"enabled"`
	Mode      string   `yaml:"mode"` // native
	Version   string   `yaml:"version"`
	Host      string   `yaml:"host"` // Host name from Hosts map
	Port      int      `yaml:"port"`
	Databases []string `yaml:"databases,omitempty"`
	SQLAccess string   `yaml:"sql_access,omitempty"` // "direct" (default) or "ssh"
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
	Name     string            `yaml:"name"`               // e.g., "foghorn", "platform"
	Engine   string            `yaml:"engine,omitempty"`   // Optional override: "valkey" or "redis"
	Host     string            `yaml:"host"`               // Host name from Hosts map
	Port     int               `yaml:"port"`               // Default: 6379
	Password string            `yaml:"password,omitempty"` // AUTH password
	Config   map[string]string `yaml:"config,omitempty"`   // maxmemory, appendonly, etc.
}

// ServiceConfig represents a FrameWorks application or interface service
type ServiceConfig struct {
	Enabled   bool              `yaml:"enabled"`
	Mode      string            `yaml:"mode"` // docker | native
	Version   string            `yaml:"version"`
	Image     string            `yaml:"image,omitempty"`      // For docker mode
	BinaryURL string            `yaml:"binary_url,omitempty"` // For native mode
	Deploy    string            `yaml:"deploy,omitempty"`     // Underlying service slug (container/binary name)
	Cluster   string            `yaml:"cluster,omitempty"`    // Explicit cluster assignment
	Host      string            `yaml:"host,omitempty"`       // Single host
	Hosts     []string          `yaml:"hosts,omitempty"`      // Multiple hosts (for replicas)
	Port      int               `yaml:"port,omitempty"`
	GRPCPort  int               `yaml:"grpc_port,omitempty"`
	Replicas  int               `yaml:"replicas,omitempty"`
	EnvFile   string            `yaml:"env_file,omitempty"`
	DependsOn []string          `yaml:"depends_on,omitempty"`
	Public    bool              `yaml:"public,omitempty"` // Has public-facing endpoint
	Config    map[string]string `yaml:"config,omitempty"` // Service-specific config
}

// EdgeManifest represents edge node deployment configuration (edges.yaml)
type EdgeManifest struct {
	Version         string     `yaml:"version"`
	Channel         string     `yaml:"channel,omitempty"` // Release channel: "stable", "rc", or explicit version (e.g., "v0.2.0-rc3")
	RootDomain      string     `yaml:"root_domain"`
	PoolDomain      string     `yaml:"pool_domain"` // Shared LB pool domain (e.g., edge.example.com)
	Email           string     `yaml:"email"`       // ACME email
	ClusterID       string     `yaml:"cluster_id,omitempty"`
	EnrollmentToken string     `yaml:"enrollment_token,omitempty"` // Token for node bootstrap
	HostsFile       string     `yaml:"hosts_file,omitempty"`       // SOPS-encrypted host inventory
	FetchCert       bool       `yaml:"fetch_cert,omitempty"`       // Fetch certs from Navigator
	Mode            string     `yaml:"mode,omitempty"`             // "docker" (default) or "native"
	Nodes           []EdgeNode `yaml:"nodes"`
}

// EdgeNode represents a single edge node in the manifest
type EdgeNode struct {
	Name       string            `yaml:"name"`                  // Unique node name (e.g., edge-us-east-1)
	SSH        string            `yaml:"ssh"`                   // SSH target (user@host)
	SSHKey     string            `yaml:"-"`                     // Populated from --ssh-key flag, not from YAML
	Subdomain  string            `yaml:"subdomain,omitempty"`   // Individual subdomain (e.g., edge-us-east-1 -> edge-us-east-1.example.com)
	Region     string            `yaml:"region,omitempty"`      // Region for registration
	Labels     map[string]string `yaml:"labels,omitempty"`      // Additional labels
	ApplyTune  bool              `yaml:"apply_tune,omitempty"`  // Apply sysctl tuning
	RegisterQM bool              `yaml:"register_qm,omitempty"` // Register in Quartermaster
	Mode       string            `yaml:"mode,omitempty"`        // Per-node mode override ("docker"|"native")
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
	ExternalIP string `yaml:"external_ip"`
	User       string `yaml:"user,omitempty"`
}

// EdgeConnection holds the sensitive SSH target for an edge node.
type EdgeConnection struct {
	SSH string `yaml:"ssh"`
}

// ResolvedMode returns the effective mode for this node, falling back to the
// manifest default, then to "docker".
func (n EdgeNode) ResolvedMode(manifestDefault string) string {
	if n.Mode != "" {
		return n.Mode
	}
	if manifestDefault != "" {
		return manifestDefault
	}
	return "docker"
}
