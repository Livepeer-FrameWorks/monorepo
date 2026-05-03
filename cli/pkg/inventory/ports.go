package inventory

import (
	"fmt"
	"sort"

	"frameworks/pkg/servicedefs"
)

type portRegistry map[string]map[int]string

func (m *Manifest) validatePortCollisions() error {
	registry := portRegistry{}

	addPort := func(host string, port int, owner string) error {
		if host == "" || port == 0 {
			return nil
		}
		if registry[host] == nil {
			registry[host] = map[int]string{}
		}
		if existing, ok := registry[host][port]; ok && existing != owner {
			return fmt.Errorf("port %d on host '%s' is used by %s and %s", port, host, existing, owner)
		}
		registry[host][port] = owner
		return nil
	}

	if m.Infrastructure.Postgres != nil && m.Infrastructure.Postgres.Enabled {
		port := m.Infrastructure.Postgres.EffectivePort()
		if err := addPort(m.Infrastructure.Postgres.Host, port, "postgres"); err != nil {
			return err
		}
		for _, node := range m.Infrastructure.Postgres.Nodes {
			owner := fmt.Sprintf("postgres-node-%d", node.ID)
			if err := addPort(node.Host, port, owner); err != nil {
				return err
			}
		}
		for _, inst := range m.Infrastructure.Postgres.Instances {
			instPort := inst.Port
			if instPort == 0 {
				instPort = 5432
			}
			if err := addPort(inst.Host, instPort, "postgres-"+inst.Name); err != nil {
				return err
			}
		}
		if m.Infrastructure.Postgres.IsYugabyte() {
			const (
				yugabyteMasterRPCPort  = 7100
				yugabyteMasterWebPort  = 7000
				yugabyteTServerRPCPort = 9100
				yugabyteTServerWebPort = 11000
				yugabyteYCQLPort       = 9042
			)

			for _, node := range m.Infrastructure.Postgres.Nodes {
				owner := fmt.Sprintf("yugabyte-node-%d", node.ID)
				masterRPCPort := node.RpcPort
				if masterRPCPort == 0 {
					masterRPCPort = yugabyteMasterRPCPort
				}
				if err := addPort(node.Host, masterRPCPort, owner+"-master-rpc"); err != nil {
					return err
				}
				if err := addPort(node.Host, yugabyteMasterWebPort, owner+"-master-web"); err != nil {
					return err
				}
				if err := addPort(node.Host, yugabyteTServerRPCPort, owner+"-tserver-rpc"); err != nil {
					return err
				}
				if err := addPort(node.Host, yugabyteTServerWebPort, owner+"-tserver-web"); err != nil {
					return err
				}
				if err := addPort(node.Host, yugabyteYCQLPort, owner+"-ycql"); err != nil {
					return err
				}
			}
		}
	}

	if m.Infrastructure.ClickHouse != nil && m.Infrastructure.ClickHouse.Enabled {
		const (
			clickhouseHTTPPort        = 8123
			clickhouseInterserverPort = 9009
		)

		port := m.Infrastructure.ClickHouse.Port
		if port == 0 {
			if defaultPort, ok := servicedefs.DefaultPort("clickhouse"); ok {
				port = defaultPort
			}
		}
		if err := addPort(m.Infrastructure.ClickHouse.Host, port, "clickhouse-native"); err != nil {
			return err
		}
		if err := addPort(m.Infrastructure.ClickHouse.Host, clickhouseHTTPPort, "clickhouse-http"); err != nil {
			return err
		}
		if err := addPort(m.Infrastructure.ClickHouse.Host, clickhouseInterserverPort, "clickhouse-interserver"); err != nil {
			return err
		}
	}

	if m.Infrastructure.Zookeeper != nil && m.Infrastructure.Zookeeper.Enabled {
		for _, node := range m.Infrastructure.Zookeeper.Ensemble {
			port := node.Port
			if port == 0 {
				if defaultPort, ok := servicedefs.DefaultPort("zookeeper"); ok {
					port = defaultPort
				}
			}
			owner := fmt.Sprintf("zookeeper-%d", node.ID)
			if err := addPort(node.Host, port, owner); err != nil {
				return err
			}
		}
	}

	if m.Infrastructure.Kafka != nil && m.Infrastructure.Kafka.Enabled {
		for _, broker := range m.Infrastructure.Kafka.Brokers {
			port := broker.Port
			if port == 0 {
				if defaultPort, ok := servicedefs.DefaultPort("kafka"); ok {
					port = defaultPort
				}
			}
			owner := fmt.Sprintf("kafka-broker-%d", broker.ID)
			if err := addPort(broker.Host, port, owner); err != nil {
				return err
			}
		}

		// Dedicated controllers
		for _, ctrl := range m.Infrastructure.Kafka.Controllers {
			port := ctrl.Port
			if port == 0 {
				port = 9093
			}
			owner := fmt.Sprintf("kafka-controller-%d", ctrl.ID)
			if err := addPort(ctrl.Host, port, owner); err != nil {
				return err
			}
		}

		// Combined mode: each broker also listens on the controller port
		if len(m.Infrastructure.Kafka.Controllers) == 0 {
			ctrlPort := m.Infrastructure.Kafka.ControllerPort
			if ctrlPort == 0 {
				ctrlPort = 9093
			}
			for _, broker := range m.Infrastructure.Kafka.Brokers {
				owner := fmt.Sprintf("kafka-broker-%d-controller", broker.ID)
				if err := addPort(broker.Host, ctrlPort, owner); err != nil {
					return err
				}
			}
		}
	}

	if err := m.registerServicePorts(addPort, m.Services, "service"); err != nil {
		return err
	}
	if err := m.registerServicePorts(addPort, m.Interfaces, "interface"); err != nil {
		return err
	}
	if err := m.registerServicePorts(addPort, m.Observability, "observability"); err != nil {
		return err
	}

	return nil
}

func (m *Manifest) registerServicePorts(addPort func(string, int, string) error, services map[string]ServiceConfig, label string) error {
	ids := make([]string, 0, len(services))
	for id := range services {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		cfg := services[id]
		if !cfg.Enabled {
			continue
		}
		hosts := resolveServiceHosts(cfg)
		if len(hosts) == 0 {
			continue
		}
		port := cfg.Port
		if port == 0 {
			if defaultPort, ok := servicedefs.DefaultPort(id); ok {
				port = defaultPort
			}
		}
		grpcPort := cfg.GRPCPort
		if grpcPort == 0 {
			if defaultPort, ok := servicedefs.DefaultGRPCPort(id); ok {
				grpcPort = defaultPort
			}
		}
		for _, host := range hosts {
			if err := addPort(host, port, fmt.Sprintf("%s:%s", label, id)); err != nil {
				return err
			}
			if grpcPort != 0 && grpcPort != port {
				if err := addPort(host, grpcPort, fmt.Sprintf("%s:%s-grpc", label, id)); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func resolveServiceHosts(cfg ServiceConfig) []string {
	if cfg.Host != "" {
		return []string{cfg.Host}
	}
	return cfg.Hosts
}
