// Package bootstrap holds Quartermaster's reconcilers for the bootstrap-desired-state
// schema (see docs/architecture/bootstrap-desired-state.md). Both `quartermaster
// bootstrap` and the gRPC handlers delegate to these reconcilers so there is one
// source of truth per QM-owned table.
package bootstrap

// DesiredState is the slice of the rendered bootstrap file Quartermaster owns.
// Other top-level sections (purser, accounts, …) are ignored at decode time.
type DesiredState struct {
	Quartermaster QuartermasterSection `yaml:"quartermaster,omitempty"`
}

// SystemTenantAlias is the canonical alias for the platform/system tenant.
// Reserved — customer tenants must not reuse it.
const SystemTenantAlias = "frameworks"

type QuartermasterSection struct {
	SystemTenant              *Tenant                    `yaml:"system_tenant,omitempty"`
	Tenants                   []Tenant                   `yaml:"tenants,omitempty"`
	Clusters                  []Cluster                  `yaml:"clusters,omitempty"`
	Nodes                     []Node                     `yaml:"nodes,omitempty"`
	Mesh                      *Mesh                      `yaml:"mesh,omitempty"`
	Ingress                   IngressSection             `yaml:"ingress,omitempty"`
	ServiceRegistry           []ServiceRegistryEntry     `yaml:"service_registry,omitempty"`
	SystemTenantClusterAccess *SystemTenantClusterAccess `yaml:"system_tenant_cluster_access,omitempty"`
}

// Tenant is reconciled into quartermaster.tenants. Stable key: Alias (mapped to
// the auto-generated UUID via quartermaster.bootstrap_tenant_aliases).
type Tenant struct {
	Alias          string `yaml:"alias"`
	Name           string `yaml:"name"`
	DeploymentTier string `yaml:"deployment_tier,omitempty"`
	PrimaryColor   string `yaml:"primary_color,omitempty"`
	SecondaryColor string `yaml:"secondary_color,omitempty"`
}

// TenantRef is a path-style reference: `quartermaster.system_tenant` or
// `quartermaster.tenants[<alias>]`.
type TenantRef struct {
	Ref string `yaml:"ref"`
}

// Cluster reconciled into quartermaster.infrastructure_clusters. Stable keys: ID,
// OwnerTenant, Mesh.CIDR (when the row exists). Drift = fail.
type Cluster struct {
	ID                 string      `yaml:"id"`
	Name               string      `yaml:"name"`
	Type               string      `yaml:"type"`
	Region             string      `yaml:"region,omitempty"`
	OwnerTenant        TenantRef   `yaml:"owner_tenant"`
	IsDefault          bool        `yaml:"is_default,omitempty"`
	IsPlatformOfficial bool        `yaml:"is_platform_official,omitempty"`
	BaseURL            string      `yaml:"base_url,omitempty"`
	Mesh               ClusterMesh `yaml:"mesh"`
}

type ClusterMesh struct {
	CIDR       string `yaml:"cidr"`
	ListenPort int    `yaml:"listen_port"`
}

// Node reconciled into quartermaster.infrastructure_nodes. Stable: ID, ClusterID,
// ExternalIP, WireGuard.IP. Drift = fail.
type Node struct {
	ID         string        `yaml:"id"`
	ClusterID  string        `yaml:"cluster_id"`
	Type       string        `yaml:"type"`
	ExternalIP string        `yaml:"external_ip"`
	WireGuard  NodeWireGuard `yaml:"wireguard"`
}

type NodeWireGuard struct {
	IP        string `yaml:"ip"`
	PublicKey string `yaml:"public_key"`
	Port      int    `yaml:"port,omitempty"`
}

// Mesh is the per-cluster mesh config. Stable on CIDR.
type Mesh struct {
	CIDR            string `yaml:"cidr"`
	ListenPort      int    `yaml:"listen_port,omitempty"`
	ManageHostsFile bool   `yaml:"manage_hosts_file,omitempty"`
}

// IngressSection groups TLS bundles + ingress sites.
type IngressSection struct {
	TLSBundles []TLSBundle   `yaml:"tls_bundles,omitempty"`
	Sites      []IngressSite `yaml:"sites,omitempty"`
}

type TLSBundle struct {
	ID        string   `yaml:"id"`
	ClusterID string   `yaml:"cluster_id"`
	Domains   []string `yaml:"domains"`
	Issuer    string   `yaml:"issuer,omitempty"`
	Email     string   `yaml:"email,omitempty"`
}

type IngressSite struct {
	ID          string          `yaml:"id"`
	ClusterID   string          `yaml:"cluster_id"`
	NodeID      string          `yaml:"node_id"`
	Domains     []string        `yaml:"domains"`
	TLSBundleID string          `yaml:"tls_bundle_id"`
	Kind        string          `yaml:"kind"`
	Upstream    IngressUpstream `yaml:"upstream"`
}

type IngressUpstream struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type ServiceRegistryEntry struct {
	ServiceName    string            `yaml:"service_name"`
	Type           string            `yaml:"type"`
	Protocol       string            `yaml:"protocol,omitempty"`
	ClusterID      string            `yaml:"cluster_id"`
	NodeID         string            `yaml:"node_id"`
	Port           int               `yaml:"port"`
	HealthEndpoint string            `yaml:"health_endpoint,omitempty"`
	Metadata       map[string]string `yaml:"metadata,omitempty"`
}

type SystemTenantClusterAccess struct {
	DefaultClusters          bool `yaml:"default_clusters"`
	PlatformOfficialClusters bool `yaml:"platform_official_clusters"`
}

// Result reports per-row reconciler outcomes so callers can assert idempotency.
type Result struct {
	Created []string
	Updated []string
	Noop    []string
}

func (r Result) Total() int { return len(r.Created) + len(r.Updated) + len(r.Noop) }
