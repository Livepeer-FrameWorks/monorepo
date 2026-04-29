package provisioner

import (
	"fmt"

	"frameworks/cli/pkg/ssh"
)

// ServicePorts maps service names to their default ports.
var ServicePorts = map[string]int{
	"postgres":         5432,
	"kafka":            9092,
	"kafka-controller": 9093,
	"zookeeper":        2181,
	"clickhouse":       9000,
	"listmonk":         9001,
	"bridge":           18000,
	"commodore":        18001,
	"quartermaster":    18002,
	"purser":           18003,
	"periscope-query":  18004,
	"periscope-ingest": 18005,
	"decklog":          18006,
	"helmsman":         18007,
	"foghorn":          18008,
	"signalman":        18009,
	"navigator":        18010,
	"prometheus":       9090,
	"victoriametrics":  8428,
	"vmauth":           8427,
	"vmagent":          8429,
	"grafana":          3000,
	"metabase":         3001,
	"chartroom":        18030,
	"foredeck":         18031,
	"steward":          18032,
	"logbook":          18033,
	"skipper":          18018,
	"caddy":            18090,
	"nginx":            18090,
	"mistserver":       8080,
	"privateer":        18012,
	"redis":            6379,
	"chatwoot":         18092,
	"yugabyte":         5433,
	"deckhand":         18015,
	"livepeer-gateway": 8935,
	"livepeer-signer":  18016,
	"chandler":         18020,
}

// GetProvisioner returns the role-based provisioner for serviceName. Every
// provisioner routes through RolePlaybookProvisioner; there is no legacy
// fallback. Generic FrameWorks Go microservices use ServiceRoleProvisioner
// which dispatches compose_stack.yml (docker mode) or go_service.yml (native
// mode) based on ServiceConfig.Mode.
func GetProvisioner(serviceName string, pool *ssh.Pool) (Provisioner, error) {
	port, ok := ServicePorts[serviceName]
	if !ok && serviceName != "periscope-ingest" {
		return nil, fmt.Errorf("unknown service: %s", serviceName)
	}

	switch serviceName {
	// Infrastructure services with dedicated roles.
	case "postgres":
		return NewRolePlaybookProvisioner("postgres", pool,
			"frameworks.infra.postgres", "playbooks/postgres.yml",
			postgresRoleVars, postgresRoleDetect)
	case "kafka":
		return NewRolePlaybookProvisioner("kafka", pool,
			"frameworks.infra.kafka", "playbooks/kafka.yml",
			kafkaRoleVarsFor("broker"), kafkaRoleDetectFor("broker"))
	case "kafka-controller":
		return NewRolePlaybookProvisioner("kafka-controller", pool,
			"frameworks.infra.kafka", "playbooks/kafka.yml",
			kafkaRoleVarsFor("controller"), kafkaRoleDetectFor("controller"))
	case "zookeeper":
		return NewRolePlaybookProvisioner("zookeeper", pool,
			"frameworks.infra.zookeeper", "playbooks/zookeeper.yml",
			zookeeperRoleVars, zookeeperRoleDetect)
	case "clickhouse":
		return NewRolePlaybookProvisioner("clickhouse", pool,
			"frameworks.infra.clickhouse", "playbooks/clickhouse.yml",
			clickhouseRoleVars, clickhouseRoleDetect)
	case "yugabyte":
		return NewRolePlaybookProvisioner("yugabyte", pool,
			"frameworks.infra.yugabyte", "playbooks/yugabyte.yml",
			yugabyteRoleVars, yugabyteRoleDetect)
	case "redis":
		return NewRolePlaybookProvisioner("redis", pool,
			"frameworks.infra.redis", "playbooks/redis.yml",
			redisRoleVars, redisRoleDetect)
	case "privateer":
		return NewRolePlaybookProvisioner("privateer", pool,
			"frameworks.infra.privateer", "playbooks/privateer.yml",
			privateerRoleVars, privateerRoleDetect)
	case "caddy":
		return NewReverseProxyProvisioner("caddy", port, pool)
	case "nginx":
		return NewReverseProxyProvisioner("nginx", port, pool)

	// Observability stack — all four components route through prometheus_stack.
	case "prometheus", "victoriametrics", "vmagent", "vmauth":
		return NewRolePlaybookProvisioner(serviceName, pool,
			"frameworks.infra.prometheus_stack", "playbooks/prometheus_stack.yml",
			prometheusStackRoleVars, prometheusStackRoleDetect)

	// Compose-based interfaces with service-specific env rendering.
	case "listmonk":
		return NewRolePlaybookProvisioner("listmonk", pool,
			"frameworks.infra.listmonk", "playbooks/listmonk.yml",
			listmonkRoleVars, listmonkRoleDetect)
	case "chatwoot":
		return NewRolePlaybookProvisioner("chatwoot", pool,
			"frameworks.infra.chatwoot", "playbooks/chatwoot.yml",
			chatwootRoleVars, chatwootRoleDetect)

	// Generic FrameWorks Go microservices — dispatch on ServiceConfig.Mode.
	case "quartermaster", "commodore", "bridge", "foghorn", "decklog", "helmsman",
		"periscope-ingest", "periscope-query", "signalman", "purser", "steward",
		"navigator", "chartroom", "foredeck", "logbook", "skipper", "chandler",
		"deckhand", "metabase", "grafana",
		"livepeer-gateway", "livepeer-signer":
		cfg := ServiceRoleConfig{
			ServiceName: serviceName,
			DefaultPort: port,
		}
		if serviceName == "livepeer-gateway" || serviceName == "livepeer-signer" {
			cfg.DebianRuntimePackages = []string{"libva-drm2"}
			cfg.PacmanRuntimePackages = []string{"libva"}
			stateDir := fmt.Sprintf("/var/lib/frameworks/%s", serviceName)
			cfg.StateDirs = []string{stateDir, stateDir + "/keystore"}
		}
		return NewServiceRoleProvisioner(cfg, pool)

	default:
		return nil, fmt.Errorf("provisioner not implemented for service: %s", serviceName)
	}
}

// ListServices returns all known services.
func ListServices() []string {
	services := make([]string, 0, len(ServicePorts))
	for name := range ServicePorts {
		services = append(services, name)
	}
	return services
}
