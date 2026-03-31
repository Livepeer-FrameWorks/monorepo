package inventory

import (
	"fmt"
	"strings"
)

// Manifest represents the cluster.yaml configuration
type Manifest struct {
	Version    string `yaml:"version"`
	Type       string `yaml:"type"`                  // cluster | edge
	Profile    string `yaml:"profile,omitempty"`     // control-plane | regional | analytics-only | edge-gateway
	Channel    string `yaml:"channel,omitempty"`     // release channel: "stable" (default), "rc"
	RootDomain string `yaml:"root_domain,omitempty"` // Domain for Caddy TLS and routing
	EnvFile    string `yaml:"env_file,omitempty"`    // shared env file for all services (relative to manifest dir)
	HostsFile  string `yaml:"hosts_file,omitempty"`  // SOPS-encrypted host inventory (IPs + SSH targets)

	Hosts          map[string]Host          `yaml:"hosts,omitempty"`
	Clusters       map[string]ClusterConfig `yaml:"clusters,omitempty"`
	WireGuard      *WireGuardConfig         `yaml:"wireguard,omitempty"`
	Infrastructure InfrastructureConfig     `yaml:"infrastructure,omitempty"`
	Services       map[string]ServiceConfig `yaml:"services,omitempty"`
	Interfaces     map[string]ServiceConfig `yaml:"interfaces,omitempty"`
	Observability  map[string]ServiceConfig `yaml:"observability,omitempty"`
}

// ClusterConfig defines a cluster to register in Quartermaster during provisioning
type ClusterConfig struct {
	Name   string   `yaml:"name"`
	Type   string   `yaml:"type"` // central, edge
	Region string   `yaml:"region,omitempty"`
	Roles  []string `yaml:"roles,omitempty"` // control, data, analytics, media, mesh, interface, infra, support, observability
}

// Host represents a target machine
type Host struct {
	ExternalIP string            `yaml:"external_ip"`
	User       string            `yaml:"user"`
	SSHKey     string            `yaml:"ssh_key,omitempty"`
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

// KafkaConfig represents Kafka cluster configuration
type KafkaConfig struct {
	Enabled          bool          `yaml:"enabled"`
	Mode             string        `yaml:"mode"` // native
	Version          string        `yaml:"version"`
	Brokers          []KafkaBroker `yaml:"brokers,omitempty"`
	ZookeeperConnect string        `yaml:"zookeeper_connect"`
	Topics           []KafkaTopic  `yaml:"topics,omitempty"`
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
	SSHKey     string            `yaml:"ssh_key,omitempty"`     // Path to SSH key
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
	SSHKey     string `yaml:"ssh_key,omitempty"`
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
