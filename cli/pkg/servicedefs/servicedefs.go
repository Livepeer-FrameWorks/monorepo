package servicedefs

// Service defines the canonical (brand) ID and deploy slug for a service.
// Canonical IDs are what the CLI expects in manifests and commands.
type Service struct {
	ID             string
	Deploy         string
	DefaultPort    int
	HealthPath     string
	HealthProtocol string // http|grpc
	Role           string // control|routing|analytics|mesh|interface|infra|support
}

// Services is the canonical registry keyed by CLI service ID (brand name).
var Services = map[string]Service{
	// Core control plane
	"bridge":        {ID: "bridge", Deploy: "bridge", DefaultPort: 18000, HealthPath: "/health", HealthProtocol: "http", Role: "control"},
	"commodore":     {ID: "commodore", Deploy: "commodore", DefaultPort: 18001, HealthPath: "/health", HealthProtocol: "http", Role: "control"},
	"quartermaster": {ID: "quartermaster", Deploy: "quartermaster", DefaultPort: 18002, HealthPath: "/health", HealthProtocol: "http", Role: "control"},
	"purser":        {ID: "purser", Deploy: "purser", DefaultPort: 18003, HealthPath: "/health", HealthProtocol: "http", Role: "control"},

	// Analytics (Periscope)
	"periscope-query":  {ID: "periscope-query", Deploy: "periscope-query", DefaultPort: 18004, HealthPath: "/health", HealthProtocol: "http", Role: "analytics"},
	"periscope-ingest": {ID: "periscope-ingest", Deploy: "periscope-ingest", DefaultPort: 18005, HealthPath: "/health", HealthProtocol: "http", Role: "analytics"},

	// Routing/edge control
	"decklog":   {ID: "decklog", Deploy: "decklog", DefaultPort: 18006, HealthPath: "/health", HealthProtocol: "grpc", Role: "routing"},
	"helmsman":  {ID: "helmsman", Deploy: "helmsman", DefaultPort: 18007, HealthPath: "/health", HealthProtocol: "http", Role: "routing"},
	"foghorn":   {ID: "foghorn", Deploy: "foghorn", DefaultPort: 18008, HealthPath: "/health", HealthProtocol: "http", Role: "routing"},
	"signalman": {ID: "signalman", Deploy: "signalman", DefaultPort: 18009, HealthPath: "/health", HealthProtocol: "http", Role: "routing"},

	// Infra services
	"navigator": {ID: "navigator", Deploy: "navigator", DefaultPort: 18010, HealthPath: "/health", HealthProtocol: "http", Role: "infra"},
	"privateer": {ID: "privateer", Deploy: "privateer", DefaultPort: 18012, HealthPath: "/health", HealthProtocol: "http", Role: "mesh"},

	// Surfaces (interfaces)
	"chartroom": {ID: "chartroom", Deploy: "webapp", DefaultPort: 18030, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},
	"foredeck":  {ID: "foredeck", Deploy: "website", DefaultPort: 18031, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},
	"steward":   {ID: "steward", Deploy: "forms", DefaultPort: 18032, HealthPath: "/health", HealthProtocol: "http", Role: "support"},
	"logbook":   {ID: "logbook", Deploy: "docs", DefaultPort: 18033, HealthPath: "/health", HealthProtocol: "http", Role: "interface"},

	// Infra dependencies
	"postgres":   {ID: "postgres", Deploy: "postgres", DefaultPort: 5432, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"kafka":      {ID: "kafka", Deploy: "kafka", DefaultPort: 9092, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"zookeeper":  {ID: "zookeeper", Deploy: "zookeeper", DefaultPort: 2181, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"clickhouse": {ID: "clickhouse", Deploy: "clickhouse", DefaultPort: 9000, HealthPath: "", HealthProtocol: "tcp", Role: "infra"},
	"listmonk":   {ID: "listmonk", Deploy: "listmonk", DefaultPort: 9001, HealthPath: "/health", HealthProtocol: "http", Role: "support"},
	"nginx":      {ID: "nginx", Deploy: "nginx", DefaultPort: 18090, HealthPath: "", HealthProtocol: "http", Role: "interface"},
	"caddy":      {ID: "caddy", Deploy: "caddy", DefaultPort: 18090, HealthPath: "", HealthProtocol: "http", Role: "interface"},
	"prometheus": {ID: "prometheus", Deploy: "prometheus", DefaultPort: 9090, HealthPath: "/-/healthy", HealthProtocol: "http", Role: "observability"},
	"grafana":    {ID: "grafana", Deploy: "grafana", DefaultPort: 3000, HealthPath: "/api/health", HealthProtocol: "http", Role: "observability"},
	"metabase":   {ID: "metabase", Deploy: "metabase", DefaultPort: 3001, HealthPath: "/api/health", HealthProtocol: "http", Role: "observability"},
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
	s, ok := Services[id]
	if !ok {
		return "", false
	}
	return s.Deploy, true
}

// DefaultPort returns the default port for a canonical ID.
func DefaultPort(id string) (int, bool) {
	s, ok := Services[id]
	if !ok {
		return 0, false
	}
	return s.DefaultPort, true
}
