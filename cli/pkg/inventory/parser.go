package inventory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	fwsops "frameworks/cli/pkg/sops"
	"frameworks/pkg/servicedefs"
	"gopkg.in/yaml.v3"
)

// ParseManifest parses a cluster manifest from raw YAML bytes without validation.
// Use this when you need the parsed structure (e.g. to inspect HostsFile or EnvFiles)
// before full validation.
func ParseManifest(data []byte) (*Manifest, error) {
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}
	return &manifest, nil
}

// Load reads and parses a cluster manifest file
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return manifest, nil
}

// LoadFromBytes parses and validates a cluster manifest from raw YAML bytes.
func LoadFromBytes(data []byte) (*Manifest, error) {
	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return manifest, nil
}

// LoadWithHosts reads a manifest, merges host inventory from hosts_file if set,
// and validates the fully-resolved result. For manifests without hosts_file,
// this behaves identically to Load.
func LoadWithHosts(path, ageKeyFile string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}

	if manifest.HostsFile != "" {
		hostsPath := manifest.HostsFile
		if !filepath.IsAbs(hostsPath) {
			hostsPath = filepath.Join(filepath.Dir(path), hostsPath)
		}
		inv, err := LoadHostInventory(hostsPath, ageKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load host inventory: %w", err)
		}
		if err := manifest.MergeHostInventory(inv); err != nil {
			return nil, fmt.Errorf("merge host inventory: %w", err)
		}
	}

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return manifest, nil
}

// LoadHostInventory reads and decrypts a SOPS-encrypted host inventory YAML file.
func LoadHostInventory(path, ageKeyFile string) (*HostInventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read host inventory: %w", err)
	}

	if fwsops.IsEncrypted(data) {
		data, err = fwsops.DecryptData(data, "yaml", ageKeyFile)
		if err != nil {
			return nil, fmt.Errorf("decrypt host inventory: %w", err)
		}
	}

	var inv HostInventory
	if err := yaml.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("failed to parse host inventory YAML: %w", err)
	}
	return &inv, nil
}

// MergeHostInventory populates ExternalIP, User, and SSHKey on manifest hosts
// from the given host inventory.
func (m *Manifest) MergeHostInventory(inv *HostInventory) error {
	for name, host := range m.Hosts {
		conn, ok := inv.Hosts[name]
		if !ok {
			return fmt.Errorf("host '%s' not found in host inventory", name)
		}
		host.ExternalIP = conn.ExternalIP
		if conn.User != "" {
			host.User = conn.User
		}
		if conn.SSHKey != "" {
			host.SSHKey = conn.SSHKey
		}
		m.Hosts[name] = host
	}
	return nil
}

// MergeEdgeHosts populates SSH targets on edge nodes from the host inventory.
func (m *EdgeManifest) MergeEdgeHosts(inv *HostInventory) error {
	for i, node := range m.Nodes {
		conn, ok := inv.EdgeNodes[node.Name]
		if !ok {
			return fmt.Errorf("edge node '%s' not found in host inventory", node.Name)
		}
		m.Nodes[i].SSH = conn.SSH
	}
	return nil
}

// LoadEdgeWithHosts reads an edge manifest, merges host inventory if hosts_file
// is set, and validates the result.
func LoadEdgeWithHosts(path, ageKeyFile string) (*EdgeManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read edge manifest file: %w", err)
	}

	var manifest EdgeManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse edge manifest YAML: %w", err)
	}

	if manifest.HostsFile != "" {
		hostsPath := manifest.HostsFile
		if !filepath.IsAbs(hostsPath) {
			hostsPath = filepath.Join(filepath.Dir(path), hostsPath)
		}
		inv, err := LoadHostInventory(hostsPath, ageKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load host inventory: %w", err)
		}
		if err := manifest.MergeEdgeHosts(inv); err != nil {
			return nil, fmt.Errorf("merge edge hosts: %w", err)
		}
	}

	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid edge manifest: %w", err)
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

	// Validate each host has an external_ip
	for name, host := range m.Hosts {
		if host.ExternalIP == "" {
			return fmt.Errorf("host '%s': external_ip is required", name)
		}
	}

	// Validate host references in infrastructure
	if m.Infrastructure.Postgres != nil && m.Infrastructure.Postgres.Enabled {
		for _, pgHost := range m.Infrastructure.Postgres.AllHosts() {
			if _, ok := m.Hosts[pgHost]; !ok {
				return fmt.Errorf("postgres host '%s' not found in hosts", pgHost)
			}
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
		if len(m.Infrastructure.Kafka.Brokers) < 1 {
			return fmt.Errorf("kafka requires at least 1 broker")
		}
		if m.Infrastructure.Kafka.ClusterID == "" {
			return fmt.Errorf("kafka.cluster_id is required (generate with: kafka-storage.sh random-uuid)")
		}
		seenBrokerIDs := make(map[int]bool)
		for _, broker := range m.Infrastructure.Kafka.Brokers {
			if seenBrokerIDs[broker.ID] {
				return fmt.Errorf("duplicate kafka broker id: %d", broker.ID)
			}
			seenBrokerIDs[broker.ID] = true
		}

		if len(m.Infrastructure.Kafka.Controllers) > 0 {
			if len(m.Infrastructure.Kafka.Controllers) < 3 {
				return fmt.Errorf("kafka dedicated controllers require at least 3 for quorum fault tolerance")
			}
			seenControllerIDs := make(map[int]bool)
			for _, ctrl := range m.Infrastructure.Kafka.Controllers {
				if _, ok := m.Hosts[ctrl.Host]; !ok {
					return fmt.Errorf("kafka.controller host '%s' not found in hosts", ctrl.Host)
				}
				if seenControllerIDs[ctrl.ID] {
					return fmt.Errorf("duplicate kafka controller id: %d", ctrl.ID)
				}
				if seenBrokerIDs[ctrl.ID] {
					return fmt.Errorf("kafka controller id %d conflicts with broker id", ctrl.ID)
				}
				seenControllerIDs[ctrl.ID] = true
				if ctrl.DirID == "" {
					return fmt.Errorf("kafka.controllers[%d].dir_id is required (generate with: kafka-storage.sh random-uuid)", ctrl.ID)
				}
			}
		}

		brokerCount := len(m.Infrastructure.Kafka.Brokers)
		for _, topic := range m.Infrastructure.Kafka.Topics {
			if topic.ReplicationFactor > brokerCount {
				return fmt.Errorf("kafka topic %q: replication_factor (%d) exceeds broker count (%d)",
					topic.Name, topic.ReplicationFactor, brokerCount)
			}
		}
		if m.Infrastructure.Kafka.MinInSyncReplicas > 0 {
			for _, topic := range m.Infrastructure.Kafka.Topics {
				if topic.ReplicationFactor > 0 && m.Infrastructure.Kafka.MinInSyncReplicas > topic.ReplicationFactor {
					return fmt.Errorf("kafka topic %q: min_insync_replicas (%d) exceeds topic replication_factor (%d)",
						topic.Name, m.Infrastructure.Kafka.MinInSyncReplicas, topic.ReplicationFactor)
				}
			}
		}
	}

	// Validate clusters
	defaultClusterCount := 0
	for id, cluster := range m.Clusters {
		if id == "" {
			return fmt.Errorf("cluster ID must be non-empty")
		}
		if cluster.Name == "" {
			return fmt.Errorf("cluster '%s': name is required", id)
		}
		if cluster.Type == "" {
			return fmt.Errorf("cluster '%s': type is required", id)
		}
		if cluster.Default {
			defaultClusterCount++
		}
		if cluster.OwnerTenant != "" && cluster.OwnerTenant != "frameworks" {
			uuidRe := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
			if !uuidRe.MatchString(cluster.OwnerTenant) {
				return fmt.Errorf("cluster '%s': owner_tenant must be 'frameworks' or a valid UUID, got %q", id, cluster.OwnerTenant)
			}
		}
		if cluster.Pricing != nil {
			validModels := map[string]bool{"free_unmetered": true, "metered": true, "monthly": true, "tier_inherit": true, "custom": true}
			if cluster.Pricing.Model == "" {
				return fmt.Errorf("cluster '%s': pricing.model is required when pricing block is present", id)
			}
			if !validModels[cluster.Pricing.Model] {
				return fmt.Errorf("cluster '%s': pricing.model must be one of free_unmetered, metered, monthly, tier_inherit, custom; got %q", id, cluster.Pricing.Model)
			}
		}
	}
	if defaultClusterCount > 1 {
		return fmt.Errorf("at most one cluster may have default: true (found %d)", defaultClusterCount)
	}

	// Validate host cluster references
	for hostName, host := range m.Hosts {
		if host.Cluster != "" && len(m.Clusters) > 0 {
			if _, ok := m.Clusters[host.Cluster]; !ok {
				return fmt.Errorf("host '%s' references undefined cluster '%s'", hostName, host.Cluster)
			}
		}
	}

	// Services that auto-scope to manifest hosts when no explicit placement is given
	autoScopedServices := map[string]bool{"privateer": true}

	// Validate service host references
	for name, svc := range m.Services {
		if _, ok := servicedefs.Lookup(name); !ok {
			return fmt.Errorf("unknown service '%s' (not in service registry)", name)
		}
		if _, ok := m.Interfaces[name]; ok {
			return fmt.Errorf("service '%s' also defined in interfaces (duplicate name)", name)
		}
		if _, ok := m.Observability[name]; ok {
			return fmt.Errorf("service '%s' also defined in observability (duplicate name)", name)
		}
		if svc.Enabled && svc.Host == "" && len(svc.Hosts) == 0 && !autoScopedServices[name] {
			return fmt.Errorf("service '%s' is enabled but has no host or hosts defined", name)
		}
		if svc.Cluster != "" && len(m.Clusters) > 0 {
			if _, ok := m.Clusters[svc.Cluster]; !ok {
				return fmt.Errorf("service '%s' references undefined cluster '%s'", name, svc.Cluster)
			}
		}
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
		if _, ok := servicedefs.Lookup(name); !ok {
			return fmt.Errorf("unknown interface '%s' (not in service registry)", name)
		}
		if _, ok := m.Observability[name]; ok {
			return fmt.Errorf("interface '%s' also defined in observability (duplicate name)", name)
		}
		if iface.Enabled && iface.Host == "" && len(iface.Hosts) == 0 {
			return fmt.Errorf("interface '%s' is enabled but has no host or hosts defined", name)
		}
		if iface.Host != "" {
			if _, ok := m.Hosts[iface.Host]; !ok {
				return fmt.Errorf("interface '%s' host '%s' not found in hosts", name, iface.Host)
			}
		}
		for _, h := range iface.Hosts {
			if _, ok := m.Hosts[h]; !ok {
				return fmt.Errorf("interface '%s' host '%s' not found in hosts", name, h)
			}
		}
	}

	// Validate observability services if provided
	for name, obs := range m.Observability {
		if _, ok := servicedefs.Lookup(name); !ok {
			return fmt.Errorf("unknown observability service '%s' (not in service registry)", name)
		}
		if obs.Enabled && obs.Host == "" && len(obs.Hosts) == 0 {
			return fmt.Errorf("observability '%s' is enabled but has no host or hosts defined", name)
		}
		if obs.Host != "" {
			if _, ok := m.Hosts[obs.Host]; !ok {
				return fmt.Errorf("observability '%s' host '%s' not found in hosts", name, obs.Host)
			}
		}
		for _, h := range obs.Hosts {
			if _, ok := m.Hosts[h]; !ok {
				return fmt.Errorf("observability '%s' host '%s' not found in hosts", name, h)
			}
		}
	}

	if err := m.validatePortCollisions(); err != nil {
		return err
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

// ResolveCluster returns the cluster ID for a service.
// Priority: explicit service.cluster > role-based match > single cluster > fallback.
func (m *Manifest) ResolveCluster(serviceName string) string {
	// Check explicit assignment on the service
	if svc, ok := m.Services[serviceName]; ok && svc.Cluster != "" {
		return svc.Cluster
	}
	if iface, ok := m.Interfaces[serviceName]; ok && iface.Cluster != "" {
		return iface.Cluster
	}

	if len(m.Clusters) == 0 {
		return fmt.Sprintf("%s-%s", m.Type, m.Profile)
	}

	// Role-based: match the service's role against cluster roles
	if svcDef, ok := servicedefs.Lookup(serviceName); ok && svcDef.Role != "" {
		for clusterID, cluster := range m.Clusters {
			for _, role := range cluster.Roles {
				if role == svcDef.Role {
					return clusterID
				}
			}
		}
	}

	// Single cluster defined — use it
	if len(m.Clusters) == 1 {
		for id := range m.Clusters {
			return id
		}
	}

	return fmt.Sprintf("%s-%s", m.Type, m.Profile)
}

// HostCluster returns the cluster ID for a host.
// Priority: explicit host.cluster > single-cluster shortcut > empty.
func (m *Manifest) HostCluster(hostName string) string {
	host, ok := m.Hosts[hostName]
	if !ok {
		return ""
	}
	if host.Cluster != "" {
		return host.Cluster
	}
	// Single cluster — all hosts implicitly belong to it
	if len(m.Clusters) == 1 {
		for id := range m.Clusters {
			return id
		}
	}
	return ""
}

// AllClusterIDs returns all cluster IDs from the manifest.
// If no clusters section exists, returns the single auto-generated ID.
func (m *Manifest) AllClusterIDs() []string {
	if len(m.Clusters) == 0 {
		return []string{fmt.Sprintf("%s-%s", m.Type, m.Profile)}
	}
	ids := make([]string, 0, len(m.Clusters))
	for id := range m.Clusters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Save writes a cluster manifest back to disk.
func Save(path string, manifest *Manifest) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}
	return os.WriteFile(path, data, 0644)
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
