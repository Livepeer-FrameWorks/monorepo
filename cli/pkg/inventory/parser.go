package inventory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	fwsops "frameworks/cli/pkg/sops"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
	"gopkg.in/yaml.v3"
)

func isClusterScopedBunnyDeploy(serviceName, deployName string) bool {
	serviceType := strings.TrimSpace(serviceName)
	if pkgdns.ProviderForServiceType(serviceType) != pkgdns.ProviderBunny && strings.TrimSpace(deployName) != "" {
		serviceType = strings.TrimSpace(deployName)
	}
	return pkgdns.ProviderForServiceType(serviceType) == pkgdns.ProviderBunny &&
		pkgdns.IsClusterScopedServiceType(serviceType)
}

// strictUnmarshal rejects any YAML field that isn't declared on the target
// struct. This enforces the schema at parse time — removed fields (ssh_key,
// etc.) and typos fail loudly rather than being silently dropped.
func strictUnmarshal(data []byte, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	return dec.Decode(into)
}

// ParseManifest parses a cluster manifest from raw YAML bytes without validation.
// Use this when you need the parsed structure (e.g. to inspect HostsFile or EnvFiles)
// before full validation.
func ParseManifest(data []byte) (*Manifest, error) {
	var manifest Manifest
	if err := strictUnmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}
	for name, host := range manifest.Hosts {
		host.Name = name
		manifest.Hosts[name] = host
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
	return loadWithHosts(path, "", ageKeyFile, true)
}

// LoadWithHostsNoValidate reads a manifest and merges host inventory without
// running full manifest validation. Use for GitOps editing flows that populate
// fields required by validation.
func LoadWithHostsNoValidate(path, ageKeyFile string) (*Manifest, error) {
	return loadWithHosts(path, "", ageKeyFile, false)
}

// LoadWithHostsFileNoValidate reads a manifest, merges host inventory from the
// provided path, and skips full validation. It is used by mesh mutation flows
// where --hosts-file may intentionally override the manifest's hosts_file.
func LoadWithHostsFileNoValidate(path, hostsPath, ageKeyFile string) (*Manifest, error) {
	return loadWithHosts(path, hostsPath, ageKeyFile, false)
}

func loadWithHosts(path, explicitHostsPath, ageKeyFile string, validate bool) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	manifest, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}

	// An explicitly-provided hostsPath is used verbatim — it is already
	// CWD-relative or absolute (callers like `mesh wg` pre-resolve it). Only the
	// manifest's own hosts_file field is relative to the manifest's directory;
	// re-joining an explicit path double-resolves it (clusters/x/clusters/x/...).
	hostsPath := explicitHostsPath
	if hostsPath == "" && manifest.HostsFile != "" {
		hostsPath = manifest.HostsFile
		if !filepath.IsAbs(hostsPath) {
			hostsPath = filepath.Join(filepath.Dir(path), hostsPath)
		}
	}
	if hostsPath != "" {
		inv, err := LoadHostInventory(hostsPath, ageKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load host inventory: %w", err)
		}
		if err := manifest.MergeHostInventory(inv); err != nil {
			return nil, fmt.Errorf("merge host inventory: %w", err)
		}
	}

	if validate {
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("invalid manifest: %w", err)
		}
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
	if err := strictUnmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("failed to parse host inventory YAML: %w", err)
	}
	return &inv, nil
}

// MergeHostInventory populates connection and WireGuard identity fields on
// manifest hosts from the given host inventory. Covers both the SOPS-managed
// key path (`wireguard_private_key`) and the adopted-local preserve-key
// path (`wireguard_private_key_file` + `wireguard_private_key_managed`).
func (m *Manifest) MergeHostInventory(inv *HostInventory) error {
	for name, host := range m.Hosts {
		conn, ok := inv.Hosts[name]
		if !ok {
			return fmt.Errorf("host '%s' not found in host inventory", name)
		}
		host.Name = name
		host.ExternalIP = conn.ExternalIP
		if conn.User != "" {
			host.User = conn.User
		}
		if conn.WireguardPrivateKey != "" {
			host.WireguardPrivateKey = conn.WireguardPrivateKey
		}
		if conn.WireguardPrivateKeyFile != "" {
			host.WireguardPrivateKeyFile = conn.WireguardPrivateKeyFile
		}
		if conn.WireguardPrivateKeyManaged != nil {
			managed := *conn.WireguardPrivateKeyManaged
			host.WireguardPrivateKeyManaged = &managed
		}
		m.Hosts[name] = host
	}
	return nil
}

// MergeEdgeHosts populates SSH targets and external IPs on edge nodes from the
// host inventory. Composes user@external_ip into EdgeNode.SSH and stamps
// EdgeNode.ExternalIP so registration call sites can use the canonical IP
// directly instead of round-tripping via a remote ifconfig.me probe.
func (m *EdgeManifest) MergeEdgeHosts(inv *HostInventory) error {
	for i, node := range m.Nodes {
		conn, ok := inv.EdgeNodes[node.Name]
		if !ok {
			return fmt.Errorf("edge node '%s' not found in host inventory", node.Name)
		}
		if conn.ExternalIP == "" {
			return fmt.Errorf("edge node '%s': external_ip required", node.Name)
		}
		user := conn.User
		if user == "" {
			user = "root"
		}
		m.Nodes[i].SSH = user + "@" + conn.ExternalIP
		m.Nodes[i].ExternalIP = conn.ExternalIP
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
	if err := strictUnmarshal(data, &manifest); err != nil {
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
// validateClusterNodeIDs enforces the shared identity invariant for hand-authored
// cluster node IDs (Kafka brokers/controllers, Postgres nodes, ClickHouse nodes):
// every ID is a positive integer and unique within its group. These IDs drive task
// names, Keeper/raft server_ids, and replica macros, so collisions silently corrupt
// provisioning. kind is used only for the error message.
func validateClusterNodeIDs(kind string, ids []int) error {
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("%s node id must be a positive integer, got %d", kind, id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("duplicate %s node id: %d", kind, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// validateUniqueNodeHosts rejects two nodes sharing a host. Applies only to
// topologies that run one process per host (ClickHouse, YugabyteDB) — NOT Kafka,
// which may legitimately co-locate a broker and a controller on one host.
func validateUniqueNodeHosts(kind string, hosts []string) error {
	seen := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if _, dup := seen[h]; dup {
			return fmt.Errorf("duplicate %s node host: %s", kind, h)
		}
		seen[h] = struct{}{}
	}
	return nil
}

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

	// Validate each host has an external_ip for SSH/bootstrap. WireGuard
	// identity is validated explicitly by mesh/provisioning flows so local
	// dev manifests without Privateer can still parse.
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
		if len(m.Infrastructure.Postgres.Nodes) > 0 {
			pgIDs := make([]int, 0, len(m.Infrastructure.Postgres.Nodes))
			pgNodeHosts := make([]string, 0, len(m.Infrastructure.Postgres.Nodes))
			for _, n := range m.Infrastructure.Postgres.Nodes {
				pgIDs = append(pgIDs, n.ID)
				pgNodeHosts = append(pgNodeHosts, n.Host)
			}
			if err := validateClusterNodeIDs("postgres", pgIDs); err != nil {
				return err
			}
			if err := validateUniqueNodeHosts("postgres", pgNodeHosts); err != nil {
				return err
			}
		}
		seenPostgresInstances := map[string]struct{}{}
		for _, inst := range m.Infrastructure.Postgres.Instances {
			if strings.TrimSpace(inst.Name) == "" {
				return fmt.Errorf("postgres instance name is required")
			}
			if _, exists := seenPostgresInstances[inst.Name]; exists {
				return fmt.Errorf("duplicate postgres instance '%s'", inst.Name)
			}
			seenPostgresInstances[inst.Name] = struct{}{}
			if strings.TrimSpace(inst.Host) == "" {
				return fmt.Errorf("postgres instance '%s': host is required", inst.Name)
			}
			if _, ok := m.Hosts[inst.Host]; !ok {
				return fmt.Errorf("postgres instance '%s' host '%s' not found in hosts", inst.Name, inst.Host)
			}
		}
	}

	if ch := m.Infrastructure.ClickHouse; ch != nil && ch.Enabled {
		// `host:` was removed — ClickHouse is always a Replicated cluster (N>=1).
		// Tombstone the old field with a clear migration error.
		if ch.Host != "" {
			return fmt.Errorf("clickhouse.host was removed; use clickhouse.nodes: [{host: %q, id: 1}]", ch.Host)
		}
		if len(ch.Nodes) == 0 {
			return fmt.Errorf("clickhouse: at least one entry in 'nodes' is required when enabled")
		}
		chIDs := make([]int, 0, len(ch.Nodes))
		chHosts := make([]string, 0, len(ch.Nodes))
		for _, node := range ch.Nodes {
			if _, ok := m.Hosts[node.Host]; !ok {
				return fmt.Errorf("clickhouse.node host '%s' not found in hosts", node.Host)
			}
			chIDs = append(chIDs, node.ID)
			chHosts = append(chHosts, node.Host)
		}
		if err := validateClusterNodeIDs("clickhouse", chIDs); err != nil {
			return err
		}
		if err := validateUniqueNodeHosts("clickhouse", chHosts); err != nil {
			return err
		}
		// N>1 is refused after id/host validation. The operational model permits only a
		// single ClickHouse node because bootstrap and direct ops target one coordinator;
		// rejecting larger manifests prevents silent partial apply.
		if len(ch.Nodes) > 1 {
			return fmt.Errorf("clickhouse: multi-node (%d nodes) is unsupported by this release; declare exactly one node", len(ch.Nodes))
		}
		// read_endpoint/write_endpoint are host-key overrides for the service
		// CLICKHOUSE_ADDR (migration cutover); when set they must resolve to a host.
		for _, ep := range []struct{ field, host string }{
			{"read_endpoint", ch.ReadEndpoint},
			{"write_endpoint", ch.WriteEndpoint},
		} {
			if ep.host != "" {
				if _, ok := m.Hosts[ep.host]; !ok {
					return fmt.Errorf("clickhouse.%s host '%s' not found in hosts", ep.field, ep.host)
				}
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
		// Shared id>0 + uniqueness invariant; Kafka may co-locate hosts so no
		// unique-host check here.
		brokerIDs := make([]int, 0, len(m.Infrastructure.Kafka.Brokers))
		seenBrokerIDs := make(map[int]bool)
		for _, broker := range m.Infrastructure.Kafka.Brokers {
			brokerIDs = append(brokerIDs, broker.ID)
			seenBrokerIDs[broker.ID] = true
		}
		if err := validateClusterNodeIDs("kafka broker", brokerIDs); err != nil {
			return err
		}

		if len(m.Infrastructure.Kafka.Controllers) > 0 {
			if len(m.Infrastructure.Kafka.Controllers) < 3 {
				return fmt.Errorf("kafka dedicated controllers require at least 3 for quorum fault tolerance")
			}
			ctrlIDs := make([]int, 0, len(m.Infrastructure.Kafka.Controllers))
			for _, ctrl := range m.Infrastructure.Kafka.Controllers {
				if _, ok := m.Hosts[ctrl.Host]; !ok {
					return fmt.Errorf("kafka.controller host '%s' not found in hosts", ctrl.Host)
				}
				// Kafka-specific: broker and controller IDs share one space.
				if seenBrokerIDs[ctrl.ID] {
					return fmt.Errorf("kafka controller id %d conflicts with broker id", ctrl.ID)
				}
				if ctrl.DirID == "" {
					return fmt.Errorf("kafka.controllers[%d].dir_id is required (generate with: kafka-storage.sh random-uuid)", ctrl.ID)
				}
				ctrlIDs = append(ctrlIDs, ctrl.ID)
			}
			if err := validateClusterNodeIDs("kafka controller", ctrlIDs); err != nil {
				return err
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

		// Exactly one Kafka cluster owns aggregate mirrored topics.
		validKafkaRole := func(r string) bool { return r == "" || r == "aggregator" || r == "regional" }
		if !validKafkaRole(m.Infrastructure.Kafka.Role) {
			return fmt.Errorf("kafka.role must be 'aggregator' or 'regional', got %q", m.Infrastructure.Kafka.Role)
		}
		aggregatorCount := 0
		topLevelRole := m.Infrastructure.Kafka.Role
		if topLevelRole == "" {
			topLevelRole = "aggregator"
		}
		if topLevelRole == "aggregator" {
			aggregatorCount++
		}
		seenRegionIDs := map[string]bool{}
		if m.Infrastructure.Kafka.RegionID != "" {
			seenRegionIDs[m.Infrastructure.Kafka.RegionID] = true
		}
		for i, rc := range m.Infrastructure.Kafka.Regional {
			if rc.RegionID == "" {
				return fmt.Errorf("kafka.regional[%d]: region_id is required", i)
			}
			if seenRegionIDs[rc.RegionID] {
				return fmt.Errorf("kafka.regional[%d]: duplicate region_id %q", i, rc.RegionID)
			}
			seenRegionIDs[rc.RegionID] = true
			if !validKafkaRole(rc.Role) {
				return fmt.Errorf("kafka.regional[%d] (%s): role must be 'aggregator' or 'regional', got %q", i, rc.RegionID, rc.Role)
			}
			if rc.Role == "aggregator" {
				aggregatorCount++
			}
		}
		if aggregatorCount > 1 {
			return fmt.Errorf("kafka: at most one cluster may have role 'aggregator' (found %d)", aggregatorCount)
		}
		if aggregatorCount == 0 {
			return fmt.Errorf("kafka: exactly one cluster must have role 'aggregator' (top-level defaults to aggregator when role is unset)")
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
			if !validBootstrapTenantAlias(cluster.OwnerTenant) {
				return fmt.Errorf("cluster '%s': owner_tenant must be 'frameworks' or a bootstrap tenant alias matching ^[a-z][a-z0-9-]*$, got %q", id, cluster.OwnerTenant)
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
		deployName, ok := servicedefs.DeployName(name, svc.Deploy)
		if !ok {
			return fmt.Errorf("unknown service '%s' (not in service registry)", name)
		}
		if _, ok := m.Interfaces[name]; ok {
			return fmt.Errorf("service '%s' also defined in interfaces (duplicate name)", name)
		}
		if _, ok := m.Observability[name]; ok {
			return fmt.Errorf("service '%s' also defined in observability (duplicate name)", name)
		}
		if svc.Enabled && svc.Host == "" && len(svc.Hosts) == 0 && !autoScopedServices[deployName] {
			return fmt.Errorf("service '%s' is enabled but has no host or hosts defined", name)
		}
		if svc.Cluster != "" && len(svc.Clusters) > 0 {
			return fmt.Errorf("service '%s' sets both 'cluster' and 'clusters'; use one (clusters: [...] is the M:N form, cluster: <id> is shorthand)", name)
		}
		if svc.Cluster != "" && len(m.Clusters) > 0 {
			if _, ok := m.Clusters[svc.Cluster]; !ok {
				return fmt.Errorf("service '%s' references undefined cluster '%s'", name, svc.Cluster)
			}
		}
		for _, c := range svc.Clusters {
			if len(m.Clusters) == 0 {
				break
			}
			if _, ok := m.Clusters[c]; !ok {
				return fmt.Errorf("service '%s' references undefined cluster '%s' (in clusters)", name, c)
			}
		}
		// For cluster-scoped Bunny services, any explicit cluster pin must be a
		// media/edge cluster. Physical placement (host/hosts) is independent
		// and can live on a core/control cluster — this only checks logical
		// assignment.
		if isClusterScopedBunnyDeploy(name, svc.Deploy) && len(m.Clusters) > 0 {
			pins := append([]string{}, svc.Clusters...)
			if svc.Cluster != "" {
				pins = append(pins, svc.Cluster)
			}
			for _, c := range pins {
				cfg, ok := m.Clusters[c]
				if !ok {
					continue
				}
				if cfg.Type != "edge" && !slices.Contains(cfg.Roles, "media") {
					return fmt.Errorf("service '%s' is media/cluster-scoped and cannot be pinned to cluster '%s' (cluster must be type=edge or have role=media)", name, c)
				}
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

var bootstrapTenantAliasRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func validBootstrapTenantAlias(alias string) bool {
	return bootstrapTenantAliasRE.MatchString(alias)
}

// GetHost returns a host by name
func (m *Manifest) GetHost(name string) (Host, bool) {
	host, ok := m.Hosts[name]
	return host, ok
}

// MeshAddress returns the WireGuard mesh address for hostName, or hostName if
// the host is unknown/unconfigured. Callers that need the SSH/bootstrap
// address should use GetHost(...).ExternalIP instead.
func (m *Manifest) MeshAddress(hostName string) string {
	h, ok := m.Hosts[hostName]
	if !ok {
		return hostName
	}
	if h.WireguardIP != "" {
		return h.WireguardIP
	}
	return hostName
}

// GetInfrastructureHosts returns all hosts that have infrastructure role
func (m *Manifest) GetInfrastructureHosts() []string {
	var hosts []string
	for name, host := range m.Hosts {
		if slices.Contains(host.Roles, "infrastructure") {
			hosts = append(hosts, name)
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
			if slices.Contains(cluster.Roles, svcDef.Role) {
				return clusterID
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
	if err := strictUnmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse edge manifest YAML: %w", err)
	}

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
	if m.BandwidthMbps < 0 {
		return fmt.Errorf("bandwidth_mbps must be non-negative")
	}
	if m.MaxTranscodes < 0 {
		return fmt.Errorf("max_transcodes must be non-negative")
	}
	if err := validateEdgeCapabilities(m.Capabilities); err != nil {
		return err
	}

	for i, node := range m.Nodes {
		if node.Name == "" {
			return fmt.Errorf("node[%d]: name is required", i)
		}
		if node.SSH == "" {
			return fmt.Errorf("node '%s': ssh target is required", node.Name)
		}
		if node.BandwidthMbps < 0 {
			return fmt.Errorf("node '%s': bandwidth_mbps must be non-negative", node.Name)
		}
		if node.MaxTranscodes < 0 {
			return fmt.Errorf("node '%s': max_transcodes must be non-negative", node.Name)
		}
		if err := validateEdgeCapabilities(node.Capabilities); err != nil {
			return fmt.Errorf("node '%s': %w", node.Name, err)
		}
	}

	return nil
}

func validateEdgeCapabilities(caps []string) error {
	for _, cap := range caps {
		switch strings.TrimSpace(strings.ToLower(cap)) {
		case "ingest", "edge", "storage", "processing":
		default:
			return fmt.Errorf("unsupported edge capability %q", cap)
		}
	}
	return nil
}
