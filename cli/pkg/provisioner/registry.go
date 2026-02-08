package provisioner

import (
	"fmt"

	"frameworks/cli/pkg/ssh"
)

// ServicePorts maps service names to their default ports
var ServicePorts = map[string]int{
	"postgres":         5432,
	"kafka":            9092,
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
	"grafana":          3000,
	"metabase":         3001,
	"chartroom":        18030,
	"foredeck":         18031,
	"steward":          18032,
	"logbook":          18033,
	"skipper":          18018,
	"caddy":            18090,
	"privateer":        18012,
}

// GetProvisioner returns a provisioner for a given service
func GetProvisioner(serviceName string, pool *ssh.Pool) (Provisioner, error) {
	port, ok := ServicePorts[serviceName]
	if !ok {
		// Some services (like periscope-ingest) might not have a primary HTTP port,
		// but they still need a provisioner.
		// If port is 0, it means no health check by default, but provisioner logic still runs.
		if serviceName == "periscope-ingest" { // Allow 0 port for periscope-ingest
			port = 0
			ok = true
		}
		if !ok {
			return nil, fmt.Errorf("unknown service: %s", serviceName)
		}
	}

	switch serviceName {
	case "postgres":
		return NewPostgresProvisioner(pool)
	case "kafka":
		return NewKafkaProvisioner(pool)
	case "zookeeper":
		return NewZookeeperProvisioner(pool)
	case "clickhouse":
		return NewClickHouseProvisioner(pool)
	case "privateer":
		return NewPrivateerProvisioner(pool), nil

	case "quartermaster":
		return NewFlexibleProvisioner("quartermaster", port, pool), nil
	case "commodore":
		return NewFlexibleProvisioner("commodore", port, pool), nil
	case "bridge":
		return NewFlexibleProvisioner("bridge", port, pool), nil
	case "foghorn":
		return NewFlexibleProvisioner("foghorn", port, pool), nil
	case "decklog":
		return NewFlexibleProvisioner("decklog", port, pool), nil
	case "helmsman":
		return NewFlexibleProvisioner("helmsman", port, pool), nil
	case "periscope-ingest":
		return NewFlexibleProvisioner("periscope-ingest", port, pool), nil
	case "periscope-query":
		return NewFlexibleProvisioner("periscope-query", port, pool), nil
	case "signalman":
		return NewFlexibleProvisioner("signalman", port, pool), nil
	case "purser":
		return NewFlexibleProvisioner("purser", port, pool), nil
	case "steward":
		return NewFlexibleProvisioner("steward", port, pool), nil
	case "navigator":
		return NewFlexibleProvisioner("navigator", port, pool), nil
	case "listmonk":
		return NewFlexibleProvisioner("listmonk", port, pool), nil
	case "prometheus":
		return NewFlexibleProvisioner("prometheus", port, pool), nil
	case "grafana":
		return NewFlexibleProvisioner("grafana", port, pool), nil
	case "metabase":
		return NewFlexibleProvisioner("metabase", port, pool), nil

	case "caddy":
		return NewCaddyProvisioner(pool), nil
	case "chartroom":
		return NewFlexibleProvisioner("chartroom", port, pool), nil
	case "foredeck":
		return NewFlexibleProvisioner("foredeck", port, pool), nil
	case "logbook":
		return NewFlexibleProvisioner("logbook", port, pool), nil
	case "skipper":
		return NewFlexibleProvisioner("skipper", port, pool), nil

	default:
		return nil, fmt.Errorf("provisioner not implemented for service: %s", serviceName)
	}
}

// ListServices returns all known services
func ListServices() []string {
	services := make([]string, 0, len(ServicePorts))
	for name := range ServicePorts {
		services = append(services, name)
	}
	return services
}
