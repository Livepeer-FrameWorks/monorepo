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
