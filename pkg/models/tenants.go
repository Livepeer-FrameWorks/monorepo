package models

import (
	"time"
)

// Tenant represents the full tenant record
type Tenant struct {
	ID           string  `json:"id" db:"id"`
	Name         string  `json:"name" db:"name"`
	Subdomain    *string `json:"subdomain,omitempty" db:"subdomain"`
	CustomDomain *string `json:"custom_domain,omitempty" db:"custom_domain"`

	// Branding
	LogoURL        *string `json:"logo_url,omitempty" db:"logo_url"`
	PrimaryColor   string  `json:"primary_color" db:"primary_color"`
	SecondaryColor string  `json:"secondary_color" db:"secondary_color"`

	// Deployment routing
	DeploymentTier        string   `json:"deployment_tier" db:"deployment_tier"`
	DeploymentModel       string   `json:"deployment_model" db:"deployment_model"`
	PrimaryDeploymentTier string   `json:"primary_deployment_tier" db:"primary_deployment_tier"`
	PrimaryClusterID      *string  `json:"primary_cluster_id,omitempty" db:"primary_cluster_id"`
	KafkaTopicPrefix      *string  `json:"kafka_topic_prefix,omitempty" db:"kafka_topic_prefix"`
	KafkaBrokers          []string `json:"kafka_brokers,omitempty" db:"kafka_brokers"`
	DatabaseURL           *string  `json:"database_url,omitempty" db:"database_url"`

	// Status
	IsActive bool `json:"is_active" db:"is_active"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// GraphQL union type marker method for Tenant
func (Tenant) IsUpdateTenantResult() {}

// TenantClusterAccess represents a tenant's access to a specific cluster
type TenantClusterAccess struct {
	ID        string `json:"id" db:"id"`
	TenantID  string `json:"tenant_id" db:"tenant_id"`
	ClusterID string `json:"cluster_id" db:"cluster_id"`

	// Access configuration
	AccessLevel    string `json:"access_level" db:"access_level"`
	ResourceLimits JSONB  `json:"resource_limits" db:"resource_limits"`

	// Usage tracking
	CurrentUsage JSONB `json:"current_usage" db:"current_usage"`
	QuotaUsage   JSONB `json:"quota_usage" db:"quota_usage"`

	// Access control
	IsActive  bool       `json:"is_active" db:"is_active"`
	GrantedAt time.Time  `json:"granted_at" db:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty" db:"expires_at"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// ClusterTierSupport represents which tiers a cluster supports
type ClusterTierSupport struct {
	ID        string `json:"id" db:"id"`
	ClusterID string `json:"cluster_id" db:"cluster_id"`
	TierID    string `json:"tier_id" db:"tier_id"`

	// Cluster-specific tier configuration
	TierConfig         JSONB   `json:"tier_config" db:"tier_config"`
	CapacityAllocation float64 `json:"capacity_allocation" db:"capacity_allocation"`
	PriorityLevel      int     `json:"priority_level" db:"priority_level"`

	// Availability
	IsAvailable    bool       `json:"is_available" db:"is_available"`
	EffectiveFrom  time.Time  `json:"effective_from" db:"effective_from"`
	EffectiveUntil *time.Time `json:"effective_until,omitempty" db:"effective_until"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}
