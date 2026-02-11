package inventory

// Manifest represents the cluster.yaml configuration
type Manifest struct {
	Version string `yaml:"version"`
	Type    string `yaml:"type"`              // cluster | edge
	Profile string `yaml:"profile,omitempty"` // control-plane | regional | analytics-only | edge-gateway

	Hosts          map[string]Host          `yaml:"hosts,omitempty"`
	WireGuard      *WireGuardConfig         `yaml:"wireguard,omitempty"`
	Infrastructure InfrastructureConfig     `yaml:"infrastructure,omitempty"`
	Services       map[string]ServiceConfig `yaml:"services,omitempty"`
	Interfaces     map[string]ServiceConfig `yaml:"interfaces,omitempty"`
	Observability  map[string]ServiceConfig `yaml:"observability,omitempty"`
}

// Host represents a target machine
type Host struct {
	Address    string            `yaml:"address"`
	ExternalIP string            `yaml:"external_ip,omitempty"`
	User       string            `yaml:"user"`
	SSHKey     string            `yaml:"ssh_key,omitempty"`
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
	Enabled   bool              `yaml:"enabled"`
	Mode      string            `yaml:"mode"` // native (only supported mode for infrastructure)
	Version   string            `yaml:"version"`
	Host      string            `yaml:"host"` // Host name from Hosts map
	Port      int               `yaml:"port"`
	Databases []DatabaseConfig  `yaml:"databases,omitempty"`
	Tuning    map[string]string `yaml:"tuning,omitempty"`
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
}

// RedisConfig represents Redis instance configuration
type RedisConfig struct {
	Enabled   bool            `yaml:"enabled"`
	Mode      string          `yaml:"mode"`    // "docker" or "native"
	Version   string          `yaml:"version"` // e.g., "7"
	Instances []RedisInstance `yaml:"instances"`
}

// RedisInstance represents a single named Redis instance
type RedisInstance struct {
	Name     string            `yaml:"name"`               // e.g., "foghorn", "platform"
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
	RootDomain      string     `yaml:"root_domain"`
	PoolDomain      string     `yaml:"pool_domain"` // Shared LB pool domain (e.g., edge-egress.example.com)
	Email           string     `yaml:"email"`       // ACME email
	ClusterID       string     `yaml:"cluster_id,omitempty"`
	EnrollmentToken string     `yaml:"enrollment_token,omitempty"` // Token for node bootstrap
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
