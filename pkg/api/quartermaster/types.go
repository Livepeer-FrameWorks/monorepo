package quartermaster

import (
	"time"

	"frameworks/pkg/api/common"
	"frameworks/pkg/models"
)

// ValidateTenantResponse represents the response from the validate tenant API
type ValidateTenantResponse = models.ValidateTenantResponse

// ResolveTenantResponse represents the response from the resolve tenant API
type ResolveTenantResponse = models.ResolveTenantResponse

// ResolveTenantRequest represents the request to resolve tenant API
type ResolveTenantRequest = models.ResolveTenantRequest

// TenantRoutingRequest represents a request for tenant routing
type TenantRoutingRequest struct {
	TenantID string `json:"tenant_id"`
	StreamID string `json:"stream_id"`
}

// TenantRoutingResponse uses the shared ClusterRouting model
type TenantRoutingResponse = models.ClusterRouting

// TenantInfo represents basic tenant information
type TenantInfo struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	IsActive         bool    `json:"is_active"`
	Domain           string  `json:"domain,omitempty"`
	PrimaryClusterID *string `json:"primary_cluster_id,omitempty"`
}

// GetTenantResponse represents the response from get tenant API
type GetTenantResponse struct {
	Tenant *TenantInfo `json:"tenant,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// ValidateTenantRequest represents a request to validate tenant
type ValidateTenantRequest struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
}

// CreateTenantRequest represents a request to create a tenant (moved from pkg/models)
type CreateTenantRequest struct {
	Name                   string   `json:"name" binding:"required"`
	Subdomain              *string  `json:"subdomain,omitempty"`
	CustomDomain           *string  `json:"custom_domain,omitempty"`
	LogoURL                *string  `json:"logo_url,omitempty"`
	PrimaryColor           string   `json:"primary_color"`
	SecondaryColor         string   `json:"secondary_color"`
	DeploymentTier         string   `json:"deployment_tier"`
	DeploymentModel        string   `json:"deployment_model"`
	PrimaryDeploymentTier  string   `json:"primary_deployment_tier"`
	AllowedDeploymentTiers []string `json:"allowed_deployment_tiers,omitempty"`
}

// CreateTenantResponse represents the response from create tenant API
type CreateTenantResponse struct {
	Tenant models.Tenant `json:"tenant"`
	Error  string        `json:"error,omitempty"`
}

// ErrorResponse is a type alias to the common error response
type ErrorResponse = common.ErrorResponse

// SuccessResponse is a type alias to the common success response
type SuccessResponse = common.SuccessResponse

// Tenant routing response wrapper
type GetRoutingResponse struct {
	Routing models.ClusterRouting `json:"routing"`
}

// Get tenants response
type GetTenantsResponse struct {
	Tenants []models.Tenant `json:"tenants"`
}

// Get tenants by cluster response
type GetTenantsByClusterResponse struct {
	Tenants   []models.Tenant `json:"tenants"`
	ClusterID string          `json:"cluster_id"`
}

// Single tenant response for GET /tenant/:id
type SingleTenantResponse struct {
	Tenant models.Tenant `json:"tenant"`
}

// Update tenant cluster response
type UpdateTenantClusterResponse struct {
	Message string `json:"message"`
}

// === TENANT MANAGEMENT ===

// GrantClusterAccessRequest represents a request to grant cluster access
type GrantClusterAccessRequest struct {
	TenantID       string       `json:"tenant_id" binding:"required"`
	ClusterID      string       `json:"cluster_id" binding:"required"`
	AccessLevel    string       `json:"access_level"`
	ResourceLimits models.JSONB `json:"resource_limits"`
	ExpiresAt      *time.Time   `json:"expires_at,omitempty"`
}

// UpdateTenantRequest represents a request to update a tenant
type UpdateTenantRequest struct {
	Name                   *string  `json:"name,omitempty"`
	Subdomain              *string  `json:"subdomain,omitempty"`
	CustomDomain           *string  `json:"custom_domain,omitempty"`
	LogoURL                *string  `json:"logo_url,omitempty"`
	PrimaryColor           *string  `json:"primary_color,omitempty"`
	SecondaryColor         *string  `json:"secondary_color,omitempty"`
	DeploymentTier         *string  `json:"deployment_tier,omitempty"`
	DeploymentModel        *string  `json:"deployment_model,omitempty"`
	PrimaryDeploymentTier  *string  `json:"primary_deployment_tier,omitempty"`
	AllowedDeploymentTiers []string `json:"allowed_deployment_tiers,omitempty"`
	PrimaryClusterID       *string  `json:"primary_cluster_id,omitempty"`
	IsActive               *bool    `json:"is_active,omitempty"`
}

// GetClusterRoutingRequest represents a request for cluster routing info
type GetClusterRoutingRequest struct {
	TenantID         string `json:"tenant_id" binding:"required"`
	StreamID         string `json:"stream_id,omitempty"`
	EstimatedViewers int    `json:"estimated_viewers,omitempty"`
	EstimatedMbps    int    `json:"estimated_mbps,omitempty"`
	Region           string `json:"region,omitempty"`
}

// === INFRASTRUCTURE MANAGEMENT ===

// Infrastructure cluster responses
type ClustersResponse struct {
	Clusters []models.InfrastructureCluster `json:"clusters"`
	Count    int                            `json:"count"`
}

type ClusterResponse struct {
	Cluster models.InfrastructureCluster `json:"cluster"`
}

// Infrastructure node responses
type NodesResponse struct {
	Nodes   []models.InfrastructureNode `json:"nodes"`
	Count   int                         `json:"count"`
	Filters NodeFilters                 `json:"filters"`
}

type NodeFilters struct {
	ClusterID string `json:"cluster_id"`
	NodeType  string `json:"node_type"`
	Region    string `json:"region"`
}

type NodeResponse struct {
	Node models.InfrastructureNode `json:"node"`
}

type NodeHealthUpdateResponse struct {
	Message string `json:"message"`
	NodeID  string `json:"node_id"`
}

// Node fingerprint resolution
type ResolveNodeFingerprintRequest struct {
	PeerIP          string   `json:"peer_ip"`
	GeoCountry      string   `json:"geo_country,omitempty"`
	GeoCity         string   `json:"geo_city,omitempty"`
	GeoLatitude     float64  `json:"geo_latitude,omitempty"`
	GeoLongitude    float64  `json:"geo_longitude,omitempty"`
	LocalIPv4       []string `json:"local_ipv4,omitempty"`
	LocalIPv6       []string `json:"local_ipv6,omitempty"`
	MacsSHA256      *string  `json:"macs_sha256,omitempty"`
	MachineIDSHA256 *string  `json:"machine_id_sha256,omitempty"`
}

type ResolveNodeFingerprintResponse struct {
	TenantID        string `json:"tenant_id"`
	CanonicalNodeID string `json:"canonical_node_id"`
}

// Service catalog responses
type ServicesResponse struct {
	Services []models.Service `json:"services"`
	Count    int              `json:"count"`
}

type ServiceResponse struct {
	Service models.Service `json:"service"`
}

// Cluster service responses
type ClusterServicesResponse struct {
	ClusterID string                  `json:"cluster_id"`
	Services  []models.ClusterService `json:"services"`
	Count     int                     `json:"count"`
}

type ClusterServiceUpdateResponse struct {
	Message   string `json:"message"`
	ClusterID string `json:"cluster_id"`
	ServiceID string `json:"service_id"`
}

// Service instance responses
type ServiceInstancesResponse struct {
	Instances []models.ServiceInstance `json:"instances"`
	Count     int                      `json:"count"`
	Filters   ServiceInstanceFilters   `json:"filters"`
}

type ServiceInstanceFilters struct {
	ClusterID string `json:"cluster_id"`
	ServiceID string `json:"service_id"`
}

// === BOOTSTRAP & DISCOVERY ===

// BootstrapEdgeNodeRequest is used by Foghorn to register a new edge node for a tenant
type BootstrapEdgeNodeRequest struct {
	Token    string                 `json:"token" binding:"required"`
	Hostname string                 `json:"hostname"`
	IPs      []string               `json:"ips,omitempty"`
	Labels   map[string]interface{} `json:"labels,omitempty"`
	// Optional fingerprint signals for identity binding at enrollment time
	LocalIPv4       []string `json:"local_ipv4,omitempty"`
	LocalIPv6       []string `json:"local_ipv6,omitempty"`
	MacsSHA256      *string  `json:"macs_sha256,omitempty"`
	MachineIDSHA256 *string  `json:"machine_id_sha256,omitempty"`
}

type BootstrapEdgeNodeResponse struct {
	NodeID    string `json:"node_id"`
	TenantID  string `json:"tenant_id"`
	ClusterID string `json:"cluster_id"`
}

// BootstrapServiceRequest is used by core services to self-register
type BootstrapServiceRequest struct {
	Token          *string `json:"token,omitempty"`
	ServiceToken   *string `json:"service_token,omitempty"`
	Type           string  `json:"type" binding:"required"` // e.g., foghorn, periscope_ingest
	Version        string  `json:"version"`
	BaseURL        *string `json:"base_url,omitempty"`
	Protocol       string  `json:"protocol,omitempty"` // http (default) | grpc
	HealthEndpoint *string `json:"health_endpoint,omitempty"`
	AdvertiseHost  *string `json:"advertise_host,omitempty"`
	Host           string  `json:"host"`
	Port           int     `json:"port"`
}

type BootstrapServiceResponse struct {
	ServiceID  string  `json:"service_id"`
	InstanceID string  `json:"instance_id"`
	ClusterID  string  `json:"cluster_id"`
	NodeID     *string `json:"node_id,omitempty"`
}

// ServiceDiscoveryResponse holds discovered instances for a given type/cluster
type ServiceDiscoveryResponse struct {
	Instances []models.ServiceInstance `json:"instances"`
	Count     int                      `json:"count"`
}

// ClusterAccessEntry represents a cluster accessible to a tenant
type ClusterAccessEntry struct {
	ClusterID      string                 `json:"cluster_id"`
	ClusterName    string                 `json:"cluster_name"`
	AccessLevel    string                 `json:"access_level"`
	ResourceLimits map[string]interface{} `json:"resource_limits"`
}

type ClustersAccessResponse struct {
	Clusters []ClusterAccessEntry `json:"clusters"`
	Count    int                  `json:"count"`
}

// Available cluster entry for onboarding UX
type AvailableClusterEntry struct {
	ClusterID   string   `json:"cluster_id"`
	ClusterName string   `json:"cluster_name"`
	Tiers       []string `json:"tiers"`
	AutoEnroll  bool     `json:"auto_enroll"`
}

type ClustersAvailableResponse struct {
	Clusters []AvailableClusterEntry `json:"clusters"`
	Count    int                     `json:"count"`
}

// === SERVICE HEALTH ===

type ServiceInstanceHealth struct {
	InstanceID      string     `json:"instance_id"`
	ServiceID       string     `json:"service_id"`
	ClusterID       string     `json:"cluster_id"`
	Protocol        string     `json:"protocol"`
	Host            *string    `json:"host,omitempty"`
	Port            int        `json:"port"`
	HealthEndpoint  *string    `json:"health_endpoint,omitempty"`
	Status          string     `json:"status"`
	LastHealthCheck *time.Time `json:"last_health_check,omitempty"`
}

type ServicesHealthResponse struct {
	Instances []ServiceInstanceHealth `json:"instances"`
	Count     int                     `json:"count"`
}

// === BOOTSTRAP TOKEN MANAGEMENT ===

type BootstrapToken struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Token      string                 `json:"token"`
	Kind       string                 `json:"kind"` // edge_node | service
	TenantID   *string                `json:"tenant_id,omitempty"`
	ClusterID  *string                `json:"cluster_id,omitempty"`
	ExpectedIP *string                `json:"expected_ip,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	UsageLimit *int                   `json:"usage_limit,omitempty"`
	UsageCount int                    `json:"usage_count"`
	ExpiresAt  time.Time              `json:"expires_at"`
	UsedAt     *time.Time             `json:"used_at,omitempty"`
	CreatedBy  *string                `json:"created_by,omitempty"`
	CreatedAt  time.Time              `json:"created_at"`
}

type CreateBootstrapTokenRequest struct {
	Name       string                 `json:"name" binding:"required"`
	Kind       string                 `json:"kind" binding:"required"`
	TenantID   *string                `json:"tenant_id,omitempty"`
	ClusterID  *string                `json:"cluster_id,omitempty"`
	ExpectedIP *string                `json:"expected_ip,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	UsageLimit *int                   `json:"usage_limit,omitempty"`
	TTL        string                 `json:"ttl"` // e.g., "24h"
}

type CreateBootstrapTokenResponse struct {
	Token BootstrapToken `json:"token"`
}

type BootstrapTokensResponse struct {
	Tokens []BootstrapToken `json:"tokens"`
	Count  int              `json:"count"`
}

// Create/Update request types
type CreateClusterRequest struct {
	ClusterID            string   `json:"cluster_id" binding:"required"`
	ClusterName          string   `json:"cluster_name" binding:"required"`
	ClusterType          string   `json:"cluster_type" binding:"required"`
	BaseURL              string   `json:"base_url" binding:"required"`
	DatabaseURL          *string  `json:"database_url,omitempty"`
	PeriscopeURL         *string  `json:"periscope_url,omitempty"`
	KafkaBrokers         []string `json:"kafka_brokers,omitempty"`
	MaxConcurrentStreams int      `json:"max_concurrent_streams"`
	MaxConcurrentViewers int      `json:"max_concurrent_viewers"`
	MaxBandwidthMbps     int      `json:"max_bandwidth_mbps"`
}

type UpdateClusterRequest struct {
	ClusterName          *string  `json:"cluster_name,omitempty"`
	BaseURL              *string  `json:"base_url,omitempty"`
	DatabaseURL          *string  `json:"database_url,omitempty"`
	PeriscopeURL         *string  `json:"periscope_url,omitempty"`
	KafkaBrokers         []string `json:"kafka_brokers,omitempty"`
	MaxConcurrentStreams *int     `json:"max_concurrent_streams,omitempty"`
	MaxConcurrentViewers *int     `json:"max_concurrent_viewers,omitempty"`
	MaxBandwidthMbps     *int     `json:"max_bandwidth_mbps,omitempty"`
	CurrentStreamCount   *int     `json:"current_stream_count,omitempty"`
	CurrentViewerCount   *int     `json:"current_viewer_count,omitempty"`
	CurrentBandwidthMbps *int     `json:"current_bandwidth_mbps,omitempty"`
	HealthStatus         *string  `json:"health_status,omitempty"`
	IsActive             *bool    `json:"is_active,omitempty"`
}

type CreateNodeRequest struct {
	NodeID             string                 `json:"node_id" binding:"required"`
	ClusterID          string                 `json:"cluster_id" binding:"required"`
	NodeName           string                 `json:"node_name" binding:"required"`
	NodeType           string                 `json:"node_type" binding:"required"`
	InternalIP         *string                `json:"internal_ip,omitempty"`
	ExternalIP         *string                `json:"external_ip,omitempty"`
	WireguardIP        *string                `json:"wireguard_ip,omitempty"`
	WireguardPublicKey *string                `json:"wireguard_public_key,omitempty"`
	Region             *string                `json:"region,omitempty"`
	AvailabilityZone   *string                `json:"availability_zone,omitempty"`
	Latitude           *float64               `json:"latitude,omitempty"`
	Longitude          *float64               `json:"longitude,omitempty"`
	CPUCores           *int                   `json:"cpu_cores,omitempty"`
	MemoryGB           *int                   `json:"memory_gb,omitempty"`
	DiskGB             *int                   `json:"disk_gb,omitempty"`
	Tags               map[string]interface{} `json:"tags,omitempty"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

type UpdateNodeHealthRequest struct {
	HealthScore *float64               `json:"health_score,omitempty"`
	Status      *string                `json:"status,omitempty"`
	CPUUsage    *float64               `json:"cpu_usage,omitempty"`
	MemoryUsage *float64               `json:"memory_usage,omitempty"`
	DiskUsage   *float64               `json:"disk_usage,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type UpdateClusterServiceRequest struct {
	DesiredState    string                 `json:"desired_state"`
	DesiredReplicas *int                   `json:"desired_replicas,omitempty"`
	ConfigBlob      map[string]interface{} `json:"config_blob,omitempty"`
	EnvironmentVars map[string]interface{} `json:"environment_vars,omitempty"`
}

// NodeOwnerResponse represents the response from node owner lookup API
type NodeOwnerResponse struct {
	NodeID        string  `json:"node_id"`
	ClusterID     string  `json:"cluster_id"`
	ClusterName   string  `json:"cluster_name"`
	OwnerTenantID *string `json:"owner_tenant_id,omitempty"`
	TenantName    *string `json:"tenant_name,omitempty"`
}
