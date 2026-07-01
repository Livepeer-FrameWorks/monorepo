package servicedefs

// Service defines the canonical (brand) ID for a service.
// Canonical IDs are what the CLI expects in manifests and commands.
type Service struct {
	ID             string
	DefaultPort    int
	HealthPath     string
	HealthProtocol string // http|grpc
	Role           string // control|data|analytics|media|mesh|interface|infra|support|observability

	// SupportsSIGHUPReload is set on services whose main package has
	// registered a ReloadCallback via pkg/server.RegisterReload. When
	// true, the go_service Ansible role renders ExecReload= in the
	// systemd unit so `systemctl reload <service>` re-fires registered
	// callbacks instead of restarting the process. Default false is the
	// safe baseline: services opt in only after wiring the callback.
	SupportsSIGHUPReload bool
}

// Services is the canonical registry keyed by CLI service ID (brand name).
var Services = map[string]Service{
	// Core control plane
	"bridge":        {ID: "bridge", DefaultPort: 18000, HealthPath: "/health", HealthProtocol: "http", Role: "control", SupportsSIGHUPReload: true},
	"commodore":     {ID: "commodore", DefaultPort: 18001, HealthPath: "/health", HealthProtocol: "http", Role: "control", SupportsSIGHUPReload: true},
	"quartermaster": {ID: "quartermaster", DefaultPort: 18002, HealthPath: "/health", HealthProtocol: "http", Role: "control", SupportsSIGHUPReload: true},
	"purser":        {ID: "purser", DefaultPort: 18003, HealthPath: "/health", HealthProtocol: "http", Role: "control", SupportsSIGHUPReload: true},

	// Analytics (Periscope)
	"periscope-query":  {ID: "periscope-query", DefaultPort: 18004, HealthPath: "/health", HealthProtocol: "http", Role: "analytics", SupportsSIGHUPReload: true},
	"periscope-ingest": {ID: "periscope-ingest", DefaultPort: 18005, HealthPath: "/health", HealthProtocol: "http", Role: "analytics", SupportsSIGHUPReload: true},

	// Data plane
	"decklog":   {ID: "decklog", DefaultPort: 18006, HealthPath: "/health", HealthProtocol: "grpc", Role: "data", SupportsSIGHUPReload: true},
	"signalman": {ID: "signalman", DefaultPort: 18009, HealthPath: "/health", HealthProtocol: "http", Role: "data", SupportsSIGHUPReload: true},

	// Media plane
	"foghorn":          {ID: "foghorn", DefaultPort: 18008, HealthPath: "/health", HealthProtocol: "http", Role: "media", SupportsSIGHUPReload: true},
	"helmsman":         {ID: "helmsman", DefaultPort: 18007, HealthPath: "/health", HealthProtocol: "http", Role: "media", SupportsSIGHUPReload: true},
	"livepeer-gateway": {ID: "livepeer-gateway", DefaultPort: 8935, HealthPath: "/healthz", HealthProtocol: "http", Role: "media"},
	"livepeer-signer":  {ID: "livepeer-signer", DefaultPort: 18016, HealthPath: "/status", HealthProtocol: "http", Role: "control"},
	"mistserver":       {ID: "mistserver", DefaultPort: 8080, HealthPath: "/metrics", HealthProtocol: "http", Role: "media"},

	// Infra services
	"navigator": {ID: "navigator", DefaultPort: 18010, HealthPath: "/health", HealthProtocol: "http", Role: "infra", SupportsSIGHUPReload: true},
	"privateer": {ID: "privateer", DefaultPort: 18012, HealthPath: "/health", HealthProtocol: "http", Role: "mesh"},

	// Assets
	"chandler": {ID: "chandler", DefaultPort: 18020, HealthPath: "/health", HealthProtocol: "http", Role: "media", SupportsSIGHUPReload: true},

	// AI / support
	"skipper":  {ID: "skipper", DefaultPort: 18018, HealthPath: "/health", HealthProtocol: "http", Role: "support", SupportsSIGHUPReload: true},
	"deckhand": {ID: "deckhand", DefaultPort: 18015, HealthPath: "/health", HealthProtocol: "http", Role: "support", SupportsSIGHUPReload: true},
	"chatwoot": {ID: "chatwoot", DefaultPort: 18092, HealthPath: "/api", HealthProtocol: "http", Role: "support"},

	// Surfaces (interfaces)
	"chartroom": {ID: "chartroom", DefaultPort: 18030, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},
	"foredeck":  {ID: "foredeck", DefaultPort: 18031, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},
	"steward":   {ID: "steward", DefaultPort: 18032, HealthPath: "/health", HealthProtocol: "http", Role: "support", SupportsSIGHUPReload: true},
	"logbook":   {ID: "logbook", DefaultPort: 18033, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},

	// Infra dependencies
	"postgres":        {ID: "postgres", DefaultPort: 5432, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"kafka":           {ID: "kafka", DefaultPort: 9092, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"clickhouse":      {ID: "clickhouse", DefaultPort: 9000, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"listmonk":        {ID: "listmonk", DefaultPort: 9001, HealthPath: "/health", HealthProtocol: "http", Role: "support"},
	"nginx":           {ID: "nginx", DefaultPort: 18090, HealthPath: "", HealthProtocol: "http", Role: "interface"},
	"caddy":           {ID: "caddy", DefaultPort: 18090, HealthPath: "", HealthProtocol: "http", Role: "interface"},
	"prometheus":      {ID: "prometheus", DefaultPort: 9090, HealthPath: "/-/healthy", HealthProtocol: "http", Role: "observability"},
	"victoriametrics": {ID: "victoriametrics", DefaultPort: 8428, HealthPath: "/health", HealthProtocol: "http", Role: "observability"},
	"vmauth":          {ID: "vmauth", DefaultPort: 8427, HealthPath: "/health", HealthProtocol: "http", Role: "observability"},
	"vmagent":         {ID: "vmagent", DefaultPort: 8429, HealthPath: "/health", HealthProtocol: "http", Role: "observability"},
	"grafana":         {ID: "grafana", DefaultPort: 3000, HealthPath: "/api/health", HealthProtocol: "http", Role: "observability"},
	"metabase":        {ID: "metabase", DefaultPort: 3001, HealthPath: "/api/health", HealthProtocol: "http", Role: "observability"},
}

// Lookup returns the service definition for a canonical ID.
func Lookup(id string) (Service, bool) {
	s, ok := Services[id]
	return s, ok
}

// DeployName resolves the deploy slug for a canonical ID with an optional override.
func DeployName(id, override string) (string, bool) {
	if override != "" {
		return override, true
	}
	_, ok := Services[id]
	if !ok {
		return "", false
	}
	return id, true
}

// DefaultPort returns the default port for a canonical ID.
func DefaultPort(id string) (int, bool) {
	s, ok := Services[id]
	if !ok {
		return 0, false
	}
	return s.DefaultPort, true
}

// ClickHouse port catalog — the single source for port-collision accounting
// (cli/pkg/inventory/ports.go), the Keeper role-var builder, and the Jinja
// templates (fed via clickhouse_keeper_*_port vars). Keeps accounting in sync
// with what clickhouse-server and the colocated standalone Keeper actually bind.
const (
	ClickHouseNativePort       = 9000 // matches Services["clickhouse"].DefaultPort
	ClickHouseHTTPPort         = 8123
	ClickHouseInterserverPort  = 9009
	ClickHouseKeeperClientPort = 9181 // Keeper client (ZooKeeper-compatible)
	ClickHouseKeeperRaftPort   = 9234 // Keeper inter-server raft
)

// PortSpec names one port a service binds, for collision accounting.
type PortSpec struct {
	Name string
	Port int
}

// ClickHousePorts returns every port a ClickHouse node binds. nativePort overrides
// the default when non-zero. clustered adds the colocated standalone Keeper's
// client + raft ports (a Replicated cluster always runs Keeper).
func ClickHousePorts(nativePort int, clustered bool) []PortSpec {
	if nativePort == 0 {
		nativePort = ClickHouseNativePort
	}
	ports := []PortSpec{
		{"clickhouse-native", nativePort},
		{"clickhouse-http", ClickHouseHTTPPort},
		{"clickhouse-interserver", ClickHouseInterserverPort},
	}
	if clustered {
		ports = append(ports,
			PortSpec{"clickhouse-keeper", ClickHouseKeeperClientPort},
			PortSpec{"clickhouse-keeper-raft", ClickHouseKeeperRaftPort},
		)
	}
	return ports
}

// SupportsSIGHUPReload reports whether the service's main package has wired
// a SIGHUP reload callback. Used by the go_service Ansible role's vars
// builder to decide whether to render `ExecReload=` in the systemd unit.
// Returns false for unknown services so an unrecognized manifest entry
// never enables reload accidentally.
func SupportsSIGHUPReload(id string) bool {
	s, ok := Services[id]
	if !ok {
		return false
	}
	return s.SupportsSIGHUPReload
}

// GRPCService defines a service's gRPC endpoint for env var generation.
type GRPCService struct {
	ServiceID string // Canonical service ID (matches manifest key)
	EnvKey    string // Env var key consumers use (e.g. PERISCOPE_GRPC_ADDR)
	Port      int    // Default gRPC port
}

// grpcServices is the canonical list of services with gRPC endpoints.
// EnvKey is what consumers actually read — no string transforms.
var grpcServices = []GRPCService{
	{ServiceID: "commodore", EnvKey: "COMMODORE_GRPC_ADDR", Port: 19001},
	{ServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Port: 19002},
	{ServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Port: 19003},
	{ServiceID: "periscope-query", EnvKey: "PERISCOPE_GRPC_ADDR", Port: 19004},
	{ServiceID: "signalman", EnvKey: "SIGNALMAN_GRPC_ADDR", Port: 19005},
	{ServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Port: 18006},
	{ServiceID: "deckhand", EnvKey: "DECKHAND_GRPC_ADDR", Port: 19006},
	{ServiceID: "skipper", EnvKey: "SKIPPER_GRPC_ADDR", Port: 19007},
	{ServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Port: 18011},
	{ServiceID: "foghorn", EnvKey: "FOGHORN_GRPC_ADDR", Port: 18019},
}

// DefaultGRPCPort returns the default gRPC port for a canonical ID, if defined.
func DefaultGRPCPort(id string) (int, bool) {
	for _, svc := range grpcServices {
		if svc.ServiceID == id {
			return svc.Port, true
		}
	}
	return 0, false
}

// GRPCServices returns the full list of gRPC service definitions.
func GRPCServices() []GRPCService {
	out := make([]GRPCService, len(grpcServices))
	copy(out, grpcServices)
	return out
}

// GRPCPorts returns a map of service ID to gRPC port (for backward compatibility).
func GRPCPorts() map[string]int {
	out := make(map[string]int, len(grpcServices))
	for _, svc := range grpcServices {
		out[svc.ServiceID] = svc.Port
	}
	return out
}

// RequiredEnvVar describes an env var that requires operator input (not auto-generated).
type RequiredEnvVar struct {
	Key        string
	SetupGuide string
}

var requiredExternalEnv = map[string][]RequiredEnvVar{
	"deckhand": {
		{Key: "CHATWOOT_API_TOKEN", SetupGuide: "Chatwoot admin > Settings > Application > Access Token"},
	},
	"navigator": {
		{Key: "ACME_EMAIL", SetupGuide: "Set the certificate contact email in shared env files"},
		{Key: "CLOUDFLARE_API_TOKEN", SetupGuide: "https://dash.cloudflare.com/profile/api-tokens"},
		{Key: "CLOUDFLARE_ZONE_ID", SetupGuide: "Cloudflare dashboard > domain > Zone ID"},
		{Key: "CLOUDFLARE_ACCOUNT_ID", SetupGuide: "Cloudflare dashboard > Account Home"},
	},
	"chatwoot": {
		{Key: "DATABASE_HOST", SetupGuide: "Enable postgres in infrastructure config"},
		{Key: "REDIS_CHATWOOT_ADDR", SetupGuide: "Add a redis instance named 'chatwoot' to infrastructure config"},
	},
	"listmonk": {
		{Key: "DATABASE_HOST", SetupGuide: "Enable postgres in infrastructure config"},
		{Key: "LISTMONK_ADMIN_USER", SetupGuide: "Set LISTMONK_ADMIN_USER in gitops config (e.g. gitops/config/production.env)"},
		{Key: "LISTMONK_ADMIN_PASSWORD", SetupGuide: "Set LISTMONK_ADMIN_PASSWORD in gitops secrets via gitops/scripts/sops-env.sh set secrets/production.env LISTMONK_ADMIN_PASSWORD <value>"},
	},
	"livepeer-gateway": {
		{Key: "eth_url", SetupGuide: "Set the network RPC in shared env files (for example ARBITRUM_RPC_ENDPOINT or LIVEPEER_ETH_URL)"},
	},
	"livepeer-signer": {
		{Key: "eth_url", SetupGuide: "Set the network RPC in shared env files (for example ARBITRUM_RPC_ENDPOINT or LIVEPEER_ETH_URL)"},
	},
}

// RequiredExternalEnv returns required env vars that need operator input for a service.
func RequiredExternalEnv(serviceID string) []RequiredEnvVar {
	return requiredExternalEnv[serviceID]
}
