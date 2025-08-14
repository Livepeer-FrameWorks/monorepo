package quartermaster

import "frameworks/pkg/models"

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

// TenantRoutingResponse represents the response from tenant routing API
type TenantRoutingResponse struct {
	ClusterID       string   `json:"cluster_id"`
	ClusterType     string   `json:"cluster_type"`
	BaseURL         string   `json:"base_url"`
	KafkaBrokers    []string `json:"kafka_brokers"`
	TopicPrefix     string   `json:"topic_prefix"`
	MaxStreams      int      `json:"max_streams"`
	CurrentStreams  int      `json:"current_streams"`
	HealthStatus    string   `json:"health_status"`
	Nodes           []string `json:"nodes"`
	LoadBalanceMode string   `json:"load_balance_mode"`
	Error           string   `json:"error,omitempty"`
}

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

// CreateTenantRequest represents a request to create a new tenant
type CreateTenantRequest struct {
	Name                   string   `json:"name"`
	Domain                 string   `json:"domain,omitempty"`
	UserID                 string   `json:"user_id,omitempty"`
	IsActive               bool     `json:"is_active,omitempty"`
	DeploymentModel        string   `json:"deployment_model,omitempty"`
	PrimaryDeploymentTier  string   `json:"primary_deployment_tier,omitempty"`
	AllowedDeploymentTiers []string `json:"allowed_deployment_tiers,omitempty"`
}

// CreateTenantResponse represents the response from create tenant API
type CreateTenantResponse struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

// ErrorResponse represents a standard error response from Quartermaster
type ErrorResponse struct {
	Error string `json:"error"`
}
