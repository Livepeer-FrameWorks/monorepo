package servicedefs

// Service defines the canonical (brand) ID for a service.
// Canonical IDs are what the CLI expects in manifests and commands.
type Service struct {
	ID             string
	DefaultPort    int
	HealthPath     string
	HealthProtocol string // http|grpc
	Role           string // control|routing|analytics|mesh|interface|infra|support
}

// Services is the canonical registry keyed by CLI service ID (brand name).
var Services = map[string]Service{
	// Core control plane
	"bridge":        {ID: "bridge", DefaultPort: 18000, HealthPath: "/health", HealthProtocol: "http", Role: "control"},
	"commodore":     {ID: "commodore", DefaultPort: 18001, HealthPath: "/health", HealthProtocol: "http", Role: "control"},
	"quartermaster": {ID: "quartermaster", DefaultPort: 18002, HealthPath: "/health", HealthProtocol: "http", Role: "control"},
	"purser":        {ID: "purser", DefaultPort: 18003, HealthPath: "/health", HealthProtocol: "http", Role: "control"},

	// Analytics (Periscope)
	"periscope-query":  {ID: "periscope-query", DefaultPort: 18004, HealthPath: "/health", HealthProtocol: "http", Role: "analytics"},
	"periscope-ingest": {ID: "periscope-ingest", DefaultPort: 18005, HealthPath: "/health", HealthProtocol: "http", Role: "analytics"},

	// Routing/edge control
	"decklog":   {ID: "decklog", DefaultPort: 18006, HealthPath: "/health", HealthProtocol: "grpc", Role: "routing"},
	"helmsman":  {ID: "helmsman", DefaultPort: 18007, HealthPath: "/health", HealthProtocol: "http", Role: "routing"},
	"foghorn":   {ID: "foghorn", DefaultPort: 18008, HealthPath: "/health", HealthProtocol: "http", Role: "routing"},
	"signalman": {ID: "signalman", DefaultPort: 18009, HealthPath: "/health", HealthProtocol: "http", Role: "routing"},

	// Infra services
	"navigator": {ID: "navigator", DefaultPort: 18010, HealthPath: "/health", HealthProtocol: "http", Role: "infra"},
	"privateer": {ID: "privateer", DefaultPort: 18012, HealthPath: "/health", HealthProtocol: "http", Role: "mesh"},

	// AI / support
	"skipper": {ID: "skipper", DefaultPort: 18018, HealthPath: "/health", HealthProtocol: "http", Role: "support"},

	// Surfaces (interfaces)
	"chartroom": {ID: "chartroom", DefaultPort: 18030, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},
	"foredeck":  {ID: "foredeck", DefaultPort: 18031, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},
	"steward":   {ID: "steward", DefaultPort: 18032, HealthPath: "/health", HealthProtocol: "http", Role: "support"},
	"logbook":   {ID: "logbook", DefaultPort: 18033, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},

	// Infra dependencies
	"postgres":   {ID: "postgres", DefaultPort: 5432, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"kafka":      {ID: "kafka", DefaultPort: 9092, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"zookeeper":  {ID: "zookeeper", DefaultPort: 2181, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"clickhouse": {ID: "clickhouse", DefaultPort: 9000, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"listmonk":   {ID: "listmonk", DefaultPort: 9001, HealthPath: "/health", HealthProtocol: "http", Role: "support"},
	"nginx":      {ID: "nginx", DefaultPort: 18090, HealthPath: "", HealthProtocol: "http", Role: "interface"},
	"caddy":      {ID: "caddy", DefaultPort: 18090, HealthPath: "", HealthProtocol: "http", Role: "interface"},
	"prometheus": {ID: "prometheus", DefaultPort: 9090, HealthPath: "/-/healthy", HealthProtocol: "http", Role: "observability"},
	"grafana":    {ID: "grafana", DefaultPort: 3000, HealthPath: "/api/health", HealthProtocol: "http", Role: "observability"},
	"metabase":   {ID: "metabase", DefaultPort: 3001, HealthPath: "/api/health", HealthProtocol: "http", Role: "observability"},
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

var defaultGRPCPorts = map[string]int{
	"commodore":       19001,
	"quartermaster":   19002,
	"purser":          19003,
	"periscope-query": 19004,
	"signalman":       19005,
	"skipper":         19007,
	"navigator":       18011,
	"foghorn":         18019,
}

// DefaultGRPCPort returns the default gRPC port for a canonical ID, if defined.
func DefaultGRPCPort(id string) (int, bool) {
	port, ok := defaultGRPCPorts[id]
	return port, ok
}
