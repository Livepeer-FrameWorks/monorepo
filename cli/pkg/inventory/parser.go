package inventory

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and parses a cluster manifest file
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	// Validate
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return &manifest, nil
}

// Validate checks the manifest for errors
func (m *Manifest) Validate() error {
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}

	if m.Type == "" {
		return fmt.Errorf("type is required (cluster or edge)")
	}

	if m.Type != "cluster" && m.Type != "edge" {
		return fmt.Errorf("type must be 'cluster' or 'edge', got: %s", m.Type)
	}

	// Validate hosts exist
	if m.Type == "cluster" && len(m.Hosts) == 0 {
		return fmt.Errorf("cluster type requires at least one host")
	}

	// Validate host references in infrastructure
	if m.Infrastructure.Postgres != nil && m.Infrastructure.Postgres.Enabled {
		if _, ok := m.Hosts[m.Infrastructure.Postgres.Host]; !ok {
			return fmt.Errorf("postgres.host '%s' not found in hosts", m.Infrastructure.Postgres.Host)
		}
	}

	if m.Infrastructure.ClickHouse != nil && m.Infrastructure.ClickHouse.Enabled {
		if _, ok := m.Hosts[m.Infrastructure.ClickHouse.Host]; !ok {
			return fmt.Errorf("clickhouse.host '%s' not found in hosts", m.Infrastructure.ClickHouse.Host)
		}
	}

	if m.Infrastructure.Zookeeper != nil && m.Infrastructure.Zookeeper.Enabled {
		for _, node := range m.Infrastructure.Zookeeper.Ensemble {
			if _, ok := m.Hosts[node.Host]; !ok {
				return fmt.Errorf("zookeeper.ensemble host '%s' not found in hosts", node.Host)
			}
		}
	}

	if m.Infrastructure.Kafka != nil && m.Infrastructure.Kafka.Enabled {
		for _, broker := range m.Infrastructure.Kafka.Brokers {
			if _, ok := m.Hosts[broker.Host]; !ok {
				return fmt.Errorf("kafka.broker host '%s' not found in hosts", broker.Host)
			}
		}
	}

	// Validate service host references
	for name, svc := range m.Services {
		if svc.Host != "" {
			if _, ok := m.Hosts[svc.Host]; !ok {
				return fmt.Errorf("service '%s' host '%s' not found in hosts", name, svc.Host)
			}
		}
		for _, h := range svc.Hosts {
			if _, ok := m.Hosts[h]; !ok {
				return fmt.Errorf("service '%s' host '%s' not found in hosts", name, h)
			}
		}
	}

	for name, iface := range m.Interfaces {
		if iface.Host != "" {
			if _, ok := m.Hosts[iface.Host]; !ok {
				return fmt.Errorf("interface '%s' host '%s' not found in hosts", name, iface.Host)
			}
		}
	}

	return nil
}

// GetHost returns a host by name
func (m *Manifest) GetHost(name string) (Host, bool) {
	host, ok := m.Hosts[name]
	return host, ok
}

// GetInfrastructureHosts returns all hosts that have infrastructure role
func (m *Manifest) GetInfrastructureHosts() []string {
	var hosts []string
	for name, host := range m.Hosts {
		for _, role := range host.Roles {
			if role == "infrastructure" {
				hosts = append(hosts, name)
				break
			}
		}
	}
	return hosts
}

// GetAllHosts returns all host names
func (m *Manifest) GetAllHosts() []string {
	hosts := make([]string, 0, len(m.Hosts))
	for name := range m.Hosts {
		hosts = append(hosts, name)
	}
	return hosts
}

// LoadEdgeManifest reads and parses an edge manifest file (edges.yaml)
func LoadEdgeManifest(path string) (*EdgeManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read edge manifest file: %w", err)
	}

	var manifest EdgeManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse edge manifest YAML: %w", err)
	}

	// Validate
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid edge manifest: %w", err)
	}

	return &manifest, nil
}

// Validate checks the edge manifest for errors
func (m *EdgeManifest) Validate() error {
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}

	if m.Type != "edge" && m.Type != "" {
		return fmt.Errorf("type must be 'edge' or empty, got: %s", m.Type)
	}

	if m.RootDomain == "" && m.PoolDomain == "" {
		return fmt.Errorf("at least one of root_domain or pool_domain is required")
	}

	if len(m.Nodes) == 0 {
		return fmt.Errorf("at least one node is required")
	}

	for i, node := range m.Nodes {
		if node.Name == "" {
			return fmt.Errorf("node[%d]: name is required", i)
		}
		if node.SSH == "" {
			return fmt.Errorf("node '%s': ssh target is required", node.Name)
		}
	}

	return nil
}
