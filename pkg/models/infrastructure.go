package models

import (
	"time"
)

// InfrastructureCluster represents a cluster in the infrastructure
type InfrastructureCluster struct {
	ID          string `json:"id" db:"id"`
	ClusterID   string `json:"cluster_id" db:"cluster_id"`
	ClusterName string `json:"cluster_name" db:"cluster_name"`
	ClusterType string `json:"cluster_type" db:"cluster_type"`

	// Ownership and tenancy
	OwnerTenantID   *string `json:"owner_tenant_id,omitempty" db:"owner_tenant_id"`
	DeploymentModel string  `json:"deployment_model" db:"deployment_model"`

	// Basic routing info
	BaseURL      string  `json:"base_url" db:"base_url"`
	DatabaseURL  *string `json:"database_url,omitempty" db:"database_url"`
	PeriscopeURL *string `json:"periscope_url,omitempty" db:"periscope_url"`

	// Infrastructure endpoints
	KafkaBrokers []string `json:"kafka_brokers,omitempty" db:"kafka_brokers"`

	// Capacity limits and current usage
	MaxConcurrentStreams int `json:"max_concurrent_streams" db:"max_concurrent_streams"`
	MaxConcurrentViewers int `json:"max_concurrent_viewers" db:"max_concurrent_viewers"`
	MaxBandwidthMbps     int `json:"max_bandwidth_mbps" db:"max_bandwidth_mbps"`
	CurrentStreamCount   int `json:"current_stream_count" db:"current_stream_count"`
	CurrentViewerCount   int `json:"current_viewer_count" db:"current_viewer_count"`
	CurrentBandwidthMbps int `json:"current_bandwidth_mbps" db:"current_bandwidth_mbps"`

	// Health
	IsActive         bool       `json:"is_active" db:"is_active"`
	IsDefaultCluster bool       `json:"is_default_cluster" db:"is_default_cluster"`
	HealthStatus     string     `json:"health_status" db:"health_status"`
	LastSeen         *time.Time `json:"last_seen,omitempty" db:"last_seen"`

	// Marketplace fields
	Visibility          string                 `json:"visibility" db:"visibility"`                       // public, unlisted, private
	PricingModel        string                 `json:"pricing_model" db:"pricing_model"`                 // free_unmetered, metered, monthly, custom, tier_inherit
	MonthlyPriceCents   int                    `json:"monthly_price_cents" db:"monthly_price_cents"`     // For monthly pricing
	MeteredRateConfig map[string]interface{} `json:"metered_rate_config,omitempty" db:"metered_rate_config"`
	RequiresApproval  bool                   `json:"requires_approval" db:"requires_approval"`
	ShortDescription  *string                `json:"short_description,omitempty" db:"short_description"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// InfrastructureNode represents a node in a cluster
type InfrastructureNode struct {
	ID                 string                 `json:"id" db:"id"`
	NodeID             string                 `json:"node_id" db:"node_id"`
	ClusterID          string                 `json:"cluster_id" db:"cluster_id"`
	NodeName           string                 `json:"node_name" db:"node_name"`
	NodeType           string                 `json:"node_type" db:"node_type"`
	InternalIP         *string                `json:"internal_ip,omitempty" db:"internal_ip"`
	ExternalIP         *string                `json:"external_ip,omitempty" db:"external_ip"`
	WireguardIP        *string                `json:"wireguard_ip,omitempty" db:"wireguard_ip"`
	WireguardPublicKey *string                `json:"wireguard_public_key,omitempty" db:"wireguard_public_key"`
	Region             *string                `json:"region,omitempty" db:"region"`
	AvailabilityZone   *string                `json:"availability_zone,omitempty" db:"availability_zone"`
	Latitude           *float64               `json:"latitude,omitempty" db:"latitude"`
	Longitude          *float64               `json:"longitude,omitempty" db:"longitude"`
	CPUCores           *int                   `json:"cpu_cores,omitempty" db:"cpu_cores"`
	MemoryGB           *int                   `json:"memory_gb,omitempty" db:"memory_gb"`
	DiskGB             *int                   `json:"disk_gb,omitempty" db:"disk_gb"`
	LastHeartbeat      *time.Time             `json:"last_heartbeat,omitempty" db:"last_heartbeat"`
	Tags               map[string]interface{} `json:"tags,omitempty" db:"tags"`
	Metadata           map[string]interface{} `json:"metadata,omitempty" db:"metadata"`
	CreatedAt          time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at" db:"updated_at"`
}

// Service represents a service in the catalog
type Service struct {
	ID              string                 `json:"id" db:"id"`
	ServiceID       string                 `json:"service_id" db:"service_id"`
	Name            string                 `json:"name" db:"name"`
	Plane           string                 `json:"plane" db:"plane"`
	Description     *string                `json:"description,omitempty" db:"description"`
	DefaultPort     *int                   `json:"default_port,omitempty" db:"default_port"`
	HealthCheckPath *string                `json:"health_check_path,omitempty" db:"health_check_path"`
	DockerImage     *string                `json:"docker_image,omitempty" db:"docker_image"`
	Version         *string                `json:"version,omitempty" db:"version"`
	Dependencies    []string               `json:"dependencies,omitempty" db:"dependencies"`
	Tags            map[string]interface{} `json:"tags,omitempty" db:"tags"`
	IsActive        bool                   `json:"is_active" db:"is_active"`
	CreatedAt       time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at" db:"updated_at"`
}

// ClusterService represents a service assignment to a cluster
type ClusterService struct {
	ID              string                 `json:"id" db:"id"`
	ClusterID       string                 `json:"cluster_id" db:"cluster_id"`
	ServiceID       string                 `json:"service_id" db:"service_id"`
	DesiredState    string                 `json:"desired_state" db:"desired_state"`
	DesiredReplicas int                    `json:"desired_replicas" db:"desired_replicas"`
	CurrentReplicas int                    `json:"current_replicas" db:"current_replicas"`
	ConfigBlob      map[string]interface{} `json:"config_blob,omitempty" db:"config_blob"`
	EnvironmentVars map[string]interface{} `json:"environment_vars,omitempty" db:"environment_vars"`
	CPULimit        *float64               `json:"cpu_limit,omitempty" db:"cpu_limit"`
	MemoryLimitMB   *int                   `json:"memory_limit_mb,omitempty" db:"memory_limit_mb"`
	HealthStatus    string                 `json:"health_status" db:"health_status"`
	LastDeployed    *time.Time             `json:"last_deployed,omitempty" db:"last_deployed"`
	CreatedAt       time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time              `json:"updated_at" db:"updated_at"`

	// Joined service details
	ServiceName  string `json:"service_name,omitempty"`
	ServicePlane string `json:"service_plane,omitempty"`
}

// ServiceInstance represents a running instance of a service
type ServiceInstance struct {
	ID              string     `json:"id" db:"id"`
	InstanceID      string     `json:"instance_id" db:"instance_id"`
	ClusterID       string     `json:"cluster_id" db:"cluster_id"`
	NodeID          *string    `json:"node_id,omitempty" db:"node_id"`
	ServiceID       string     `json:"service_id" db:"service_id"`
	Version         *string    `json:"version,omitempty" db:"version"`
	Port            *int       `json:"port,omitempty" db:"port"`
	ProcessID       *int       `json:"process_id,omitempty" db:"process_id"`
	ContainerID     *string    `json:"container_id,omitempty" db:"container_id"`
	Status          string     `json:"status" db:"status"`
	HealthStatus    string     `json:"health_status" db:"health_status"`
	StartedAt       *time.Time `json:"started_at,omitempty" db:"started_at"`
	StoppedAt       *time.Time `json:"stopped_at,omitempty" db:"stopped_at"`
	LastHealthCheck *time.Time `json:"last_health_check,omitempty" db:"last_health_check"`
	CreatedAt       time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at" db:"updated_at"`
}

// ClusterInvite represents an invitation for a tenant to join a cluster
type ClusterInvite struct {
	ID               string                 `json:"id" db:"id"`
	ClusterID        string                 `json:"cluster_id" db:"cluster_id"`
	InvitedTenantID  string                 `json:"invited_tenant_id" db:"invited_tenant_id"`
	InviteToken      string                 `json:"invite_token" db:"invite_token"`
	AccessLevel      string                 `json:"access_level" db:"access_level"`
	ResourceLimits   map[string]interface{} `json:"resource_limits,omitempty" db:"resource_limits"`
	Status           string                 `json:"status" db:"status"` // pending, accepted, expired, revoked
	CreatedBy        string                 `json:"created_by" db:"created_by"`
	CreatedAt        time.Time              `json:"created_at" db:"created_at"`
	ExpiresAt        *time.Time             `json:"expires_at,omitempty" db:"expires_at"`
	AcceptedAt       *time.Time             `json:"accepted_at,omitempty" db:"accepted_at"`
	InvitedTenantName *string               `json:"invited_tenant_name,omitempty"` // Joined field
}

// ClusterSubscription represents a tenant's subscription to a cluster with status
type ClusterSubscription struct {
	ID                 string                 `json:"id" db:"id"`
	TenantID           string                 `json:"tenant_id" db:"tenant_id"`
	ClusterID          string                 `json:"cluster_id" db:"cluster_id"`
	AccessLevel        string                 `json:"access_level" db:"access_level"`
	SubscriptionStatus string                 `json:"subscription_status" db:"subscription_status"` // pending_approval, active, suspended, rejected
	ResourceLimits     map[string]interface{} `json:"resource_limits,omitempty" db:"resource_limits"`
	RequestedAt        *time.Time             `json:"requested_at,omitempty" db:"requested_at"`
	ApprovedAt         *time.Time             `json:"approved_at,omitempty" db:"approved_at"`
	ApprovedBy         *string                `json:"approved_by,omitempty" db:"approved_by"`
	RejectionReason    *string                `json:"rejection_reason,omitempty" db:"rejection_reason"`
	ExpiresAt          *time.Time             `json:"expires_at,omitempty" db:"expires_at"`
	CreatedAt          time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at" db:"updated_at"`
	// Joined fields
	ClusterName *string `json:"cluster_name,omitempty"`
	TenantName  *string `json:"tenant_name,omitempty"`
}
