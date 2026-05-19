package topology

import "sort"

// ServiceDependency describes a runtime network edge from one FrameWorks
// service to another service. Keep this catalog tied to client construction in
// service main packages; it is consumed by provisioning and mesh DNS policy.
type ServiceDependency struct {
	TargetServiceID string
	EnvKey          string
	Transport       string
	DNSScope        string
	Optional        bool
	Purpose         string
}

// InfraDependency describes a runtime network edge from a FrameWorks service to
// an infrastructure dependency rendered by cluster provision.
type InfraDependency struct {
	Kind     string
	Provider string
	Name     string
	Optional bool
	Purpose  string
}

const (
	DNSScopeGlobal = "global"

	InfraDatabase   = "database"
	InfraKafka      = "kafka"
	InfraClickHouse = "clickhouse"
	InfraRedis      = "redis"

	InfraProviderPrimary    = "primary"
	InfraProviderAggregator = "aggregator"
	InfraProviderRegional   = "regional"
	InfraProviderNamed      = "named"
)

var serviceDependencies = map[string][]ServiceDependency{
	"bridge": {
		{TargetServiceID: "commodore", EnvKey: "COMMODORE_GRPC_ADDR", Transport: "grpc", Purpose: "stream, playback, account, and control APIs"},
		{TargetServiceID: "periscope-query", EnvKey: "PERISCOPE_GRPC_ADDR", Transport: "grpc", Purpose: "analytics GraphQL, loaders, QoE MCP tools"},
		{TargetServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Transport: "grpc", Purpose: "billing APIs, x402, webhooks"},
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "tenant, cluster, bootstrap, and registry APIs"},
		{TargetServiceID: "signalman", EnvKey: "SIGNALMAN_GRPC_ADDR", Transport: "grpc", Purpose: "realtime subscriptions"},
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", Purpose: "API/service events"},
		{TargetServiceID: "deckhand", EnvKey: "DECKHAND_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "support messaging"},
		{TargetServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "DNS/certificate operator paths"},
		{TargetServiceID: "skipper", EnvKey: "SKIPPER_SPOKE_URL", Transport: "mcp-http", Optional: true, Purpose: "ask_consultant spoke proxy"},
		{TargetServiceID: "skipper", EnvKey: "SKIPPER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "AI consultant APIs"},
	},
	"chandler": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "bootstrap and storage cluster lookup"},
	},
	"commodore": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "tenant aliases, cluster capabilities, and cluster URL cache"},
		{TargetServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Transport: "grpc", Purpose: "billing and entitlement checks"},
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", DNSScope: DNSScopeGlobal, Purpose: "service events"},
		{TargetServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "DNS/certificate operations"},
	},
	"deckhand": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "tenant and bootstrap lookups"},
		{TargetServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Transport: "grpc", Purpose: "support billing context"},
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", DNSScope: DNSScopeGlobal, Purpose: "support service events"},
	},
	"decklog": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "service bootstrap"},
	},
	"foghorn": {
		{TargetServiceID: "chandler", EnvKey: "CHANDLER_INTERNAL_URL", Transport: "http", Purpose: "cluster-local object storage API"},
		{TargetServiceID: "commodore", EnvKey: "COMMODORE_GRPC_ADDR", Transport: "grpc", Purpose: "stream and playback control"},
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", Purpose: "service and media events"},
		{TargetServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "certificate refresh"},
		{TargetServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Transport: "grpc", Purpose: "billing and playback policy checks"},
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "bootstrap, tenant cluster context, and federation peers"},
	},
	"livepeer-gateway": {
		{TargetServiceID: "decklog", EnvKey: "FRAMEWORKS_DECKLOG_GRPC_ADDR", Transport: "grpc", Purpose: "gateway telemetry events"},
		{TargetServiceID: "foghorn", EnvKey: "auth_webhook_url", Transport: "http", Purpose: "playback auth webhook"},
	},
	"navigator": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "edge address and tenant/cluster authorization lookups"},
	},
	"periscope-ingest": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "service bootstrap"},
	},
	"periscope-query": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "service bootstrap and billing helpers"},
	},
	"privateer": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "mesh sync and certificate distribution"},
		{TargetServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "certificate sync bootstrap"},
	},
	"purser": {
		{TargetServiceID: "commodore", EnvKey: "COMMODORE_GRPC_ADDR", Transport: "grpc", Purpose: "stream termination and account state updates"},
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", DNSScope: DNSScopeGlobal, Purpose: "billing service events"},
		{TargetServiceID: "periscope-query", EnvKey: "PERISCOPE_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "invoice enrichment with unique counts and geo"},
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Purpose: "tenant and cluster access lookups"},
	},
	"quartermaster": {
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", DNSScope: DNSScopeGlobal, Purpose: "tenant and infrastructure service events"},
		{TargetServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "DNS and certificate workflows"},
		{TargetServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "billing-tier reconciliation"},
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "self-bootstrap service registration"},
	},
	"signalman": {
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "service bootstrap"},
	},
	"skipper": {
		{TargetServiceID: "bridge", EnvKey: "GATEWAY_MCP_URL", Transport: "mcp-http", DNSScope: DNSScopeGlobal, Purpose: "platform MCP tools"},
		{TargetServiceID: "commodore", EnvKey: "COMMODORE_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "primary-user notifications"},
		{TargetServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Transport: "grpc", DNSScope: DNSScopeGlobal, Optional: true, Purpose: "consultant usage events and notifications"},
		{TargetServiceID: "periscope-query", EnvKey: "PERISCOPE_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "heartbeat and infrastructure diagnostics"},
		{TargetServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "tier gating and billing checks"},
		{TargetServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Transport: "grpc", Optional: true, Purpose: "cluster and infrastructure diagnostics"},
	},
}

var infraDependencies = map[string][]InfraDependency{
	"commodore":        {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "control-plane state"}},
	"decklog":          {{Kind: InfraKafka, Provider: InfraProviderRegional, Purpose: "analytics and service event bus"}},
	"foghorn":          {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "media control state"}, {Kind: InfraRedis, Provider: InfraProviderNamed, Name: "foghorn", Optional: true, Purpose: "HA relay and federation state"}},
	"navigator":        {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "DNS and certificate state"}},
	"periscope-ingest": {{Kind: InfraClickHouse, Provider: InfraProviderPrimary, Purpose: "analytics writes"}, {Kind: InfraKafka, Provider: InfraProviderAggregator, Purpose: "analytics and service event ingestion"}},
	"periscope-query":  {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "query API state"}, {Kind: InfraClickHouse, Provider: InfraProviderPrimary, Purpose: "analytics reads"}, {Kind: InfraKafka, Provider: InfraProviderAggregator, Purpose: "billing usage report publication"}},
	"purser":           {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "billing state"}, {Kind: InfraKafka, Provider: InfraProviderAggregator, Purpose: "billing usage report ingestion"}},
	"quartermaster":    {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "tenant, cluster, node, and service registry state"}},
	"signalman":        {{Kind: InfraKafka, Provider: InfraProviderRegional, Purpose: "realtime analytics and service event fanout"}},
	"skipper":          {{Kind: InfraDatabase, Provider: InfraProviderPrimary, Purpose: "knowledge and conversation state"}, {Kind: InfraKafka, Provider: InfraProviderAggregator, Optional: true, Purpose: "billing usage report publication"}},
}

// ServiceDependencies returns direct service calls made by serviceID.
func ServiceDependencies(serviceID string) []ServiceDependency {
	deps := serviceDependencies[serviceID]
	out := make([]ServiceDependency, len(deps))
	copy(out, deps)
	return out
}

// InfraDependencies returns infrastructure calls made by serviceID.
func InfraDependencies(serviceID string) []InfraDependency {
	deps := infraDependencies[serviceID]
	out := make([]InfraDependency, len(deps))
	copy(out, deps)
	return out
}

func IsInfraKind(kind string) bool {
	switch kind {
	case InfraDatabase, InfraKafka, InfraClickHouse, InfraRedis:
		return true
	default:
		return false
	}
}

// RequiredServiceEnv returns required env vars for direct service calls.
func RequiredServiceEnv(serviceID string) []ServiceDependency {
	var out []ServiceDependency
	for _, dep := range serviceDependencies[serviceID] {
		if dep.Optional || dep.EnvKey == "" {
			continue
		}
		out = append(out, dep)
	}
	return out
}

// DNSServiceDependencies returns service aliases a node running serviceID may
// need from mesh DNS. Optional edges are included because they should resolve
// when the target service exists.
func DNSServiceDependencies(serviceID string) []string {
	seen := map[string]struct{}{}
	for _, dep := range serviceDependencies[serviceID] {
		if dep.TargetServiceID != "" {
			seen[dep.TargetServiceID] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// DNSDependenciesForServices returns the union of DNS dependencies for a set of
// services running on one node.
func DNSDependenciesForServices(serviceIDs []string) []string {
	seen := map[string]struct{}{}
	for _, serviceID := range serviceIDs {
		for _, dep := range DNSServiceDependencies(serviceID) {
			seen[dep] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// GlobalDNSServiceDependencies returns service aliases that must resolve across
// provider clusters instead of only within the consumer's cluster context.
func GlobalDNSServiceDependencies(serviceID string) []string {
	seen := map[string]struct{}{}
	for _, dep := range serviceDependencies[serviceID] {
		if dep.TargetServiceID != "" && dep.DNSScope == DNSScopeGlobal {
			seen[dep.TargetServiceID] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// GlobalDNSDependenciesForServices returns the union of global DNS dependencies
// for a set of services running on one node.
func GlobalDNSDependenciesForServices(serviceIDs []string) []string {
	seen := map[string]struct{}{}
	for _, serviceID := range serviceIDs {
		for _, dep := range GlobalDNSServiceDependencies(serviceID) {
			seen[dep] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// ServiceDependents returns service types that directly call any target service.
func ServiceDependents(targetServiceIDs []string) []string {
	targets := map[string]struct{}{}
	for _, target := range targetServiceIDs {
		if target != "" {
			targets[target] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for serviceID, deps := range serviceDependencies {
		for _, dep := range deps {
			if _, ok := targets[dep.TargetServiceID]; ok {
				seen[serviceID] = struct{}{}
				break
			}
		}
	}
	return sortedKeys(seen)
}

// GlobalDNSServiceDependents returns service types whose dependency on any
// target service must be reachable across provider clusters.
func GlobalDNSServiceDependents(targetServiceIDs []string) []string {
	targets := map[string]struct{}{}
	for _, target := range targetServiceIDs {
		if target != "" {
			targets[target] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for serviceID, deps := range serviceDependencies {
		for _, dep := range deps {
			if dep.DNSScope != DNSScopeGlobal {
				continue
			}
			if _, ok := targets[dep.TargetServiceID]; ok {
				seen[serviceID] = struct{}{}
				break
			}
		}
	}
	return sortedKeys(seen)
}

// FederationPeerServices returns direct peer services that are not ordinary DNS
// dependencies. Foghorn federation dials concrete peer Foghorn addresses learned
// from Quartermaster, so mesh policy must include peer nodes without making
// foghorn.internal a global sibling-cluster alias.
func FederationPeerServices(serviceID string) []string {
	if serviceID == "foghorn" {
		return []string{"foghorn"}
	}
	return nil
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
