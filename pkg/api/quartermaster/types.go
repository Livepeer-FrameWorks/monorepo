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
