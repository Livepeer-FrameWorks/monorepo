package models

import (
	"time"
)

// === QUARTERMASTER SERVICE TYPES ===

// TenantTierInfo represents tier information for a tenant
type TenantTierInfo struct {
	Tenant        Tenant                `json:"tenant"`
	Subscription  *TenantSubscription   `json:"subscription,omitempty"`
	Tier          *BillingTier          `json:"tier,omitempty"`
	ClusterAccess []TenantClusterAccess `json:"cluster_access"`
}

// CreateTierRequest represents a request to create a billing tier
type CreateTierRequest struct {
	TierName            string  `json:"tier_name" binding:"required"`
	DisplayName         string  `json:"display_name" binding:"required"`
	Description         string  `json:"description"`
	BasePrice           float64 `json:"base_price"`
	Currency            string  `json:"currency"`
	BillingPeriod       string  `json:"billing_period"`
	BandwidthAllocation JSONB   `json:"bandwidth_allocation"`
	StorageAllocation   JSONB   `json:"storage_allocation"`
	ComputeAllocation   JSONB   `json:"compute_allocation"`
	Features            JSONB   `json:"features"`
	SupportLevel        string  `json:"support_level"`
	SLALevel            string  `json:"sla_level"`
	MeteringEnabled     bool    `json:"metering_enabled"`
	OverageRates        JSONB   `json:"overage_rates"`
	SortOrder           int     `json:"sort_order"`
	IsEnterprise        bool    `json:"is_enterprise"`
}

// UpdateTierRequest represents a request to update a billing tier
type UpdateTierRequest struct {
	DisplayName         *string  `json:"display_name,omitempty"`
	Description         *string  `json:"description,omitempty"`
	BasePrice           *float64 `json:"base_price,omitempty"`
	Currency            *string  `json:"currency,omitempty"`
	BillingPeriod       *string  `json:"billing_period,omitempty"`
	BandwidthAllocation JSONB    `json:"bandwidth_allocation,omitempty"`
	StorageAllocation   JSONB    `json:"storage_allocation,omitempty"`
	ComputeAllocation   JSONB    `json:"compute_allocation,omitempty"`
	Features            JSONB    `json:"features,omitempty"`
	SupportLevel        *string  `json:"support_level,omitempty"`
	SLALevel            *string  `json:"sla_level,omitempty"`
	MeteringEnabled     *bool    `json:"metering_enabled,omitempty"`
	OverageRates        JSONB    `json:"overage_rates,omitempty"`
	SortOrder           *int     `json:"sort_order,omitempty"`
	IsEnterprise        *bool    `json:"is_enterprise,omitempty"`
	IsActive            *bool    `json:"is_active,omitempty"`
}

// GrantClusterAccessRequest represents a request to grant cluster access
type GrantClusterAccessRequest struct {
	TenantID       string     `json:"tenant_id" binding:"required"`
	ClusterID      string     `json:"cluster_id" binding:"required"`
	AccessLevel    string     `json:"access_level"`
	ResourceLimits JSONB      `json:"resource_limits"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// CreateTenantRequest represents a request to create a tenant
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

// ClusterRouting represents cluster routing information
type ClusterRouting struct {
	ClusterID      string   `json:"cluster_id"`
	ClusterName    string   `json:"cluster_name"`
	ClusterType    string   `json:"cluster_type"`
	BaseURL        string   `json:"base_url"`
	PeriscopeURL   *string  `json:"periscope_url,omitempty"`
	DatabaseURL    *string  `json:"database_url,omitempty"`
	KafkaBrokers   []string `json:"kafka_brokers,omitempty"`
	TopicPrefix    string   `json:"topic_prefix"`
	MaxStreams     int      `json:"max_streams"`
	CurrentStreams int      `json:"current_streams"`
	HealthStatus   string   `json:"health_status"`
}

// === PURSER SERVICE TYPES ===

// BillingStatus represents the current billing status for a tenant
type BillingStatus struct {
	TenantID        string             `json:"tenant_id"`
	Subscription    TenantSubscription `json:"subscription"`
	Tier            BillingTier        `json:"tier"`
	Status          string             `json:"status"`
	NextBillingDate *time.Time         `json:"next_billing_date,omitempty"`
	PendingInvoices []Invoice          `json:"pending_invoices"`
	RecentPayments  []Payment          `json:"recent_payments"`
	UsageSummary    JSONB              `json:"usage_summary,omitempty"`
}

// CreateSubscriptionRequest represents a request to create a subscription
type CreateSubscriptionRequest struct {
	TenantID          string     `json:"tenant_id" binding:"required"`
	TierID            string     `json:"tier_id" binding:"required"`
	BillingEmail      string     `json:"billing_email" binding:"required,email"`
	PaymentMethod     string     `json:"payment_method"`
	TrialEndsAt       *time.Time `json:"trial_ends_at,omitempty"`
	CustomPricing     JSONB      `json:"custom_pricing,omitempty"`
	CustomFeatures    JSONB      `json:"custom_features,omitempty"`
	CustomAllocations JSONB      `json:"custom_allocations,omitempty"`
}

// UpdateSubscriptionRequest represents a request to update a subscription
type UpdateSubscriptionRequest struct {
	TierID            *string `json:"tier_id,omitempty"`
	BillingEmail      *string `json:"billing_email,omitempty"`
	PaymentMethod     *string `json:"payment_method,omitempty"`
	Status            *string `json:"status,omitempty"`
	CustomPricing     JSONB   `json:"custom_pricing,omitempty"`
	CustomFeatures    JSONB   `json:"custom_features,omitempty"`
	CustomAllocations JSONB   `json:"custom_allocations,omitempty"`
}

// PaymentRequest represents a payment request
type PaymentRequest struct {
	InvoiceID string  `json:"invoice_id" binding:"required"`
	Method    string  `json:"method" binding:"required"` // mollie, crypto_btc, crypto_eth, etc.
	Amount    float64 `json:"amount" binding:"required"`
	Currency  string  `json:"currency" binding:"required"`
	ReturnURL string  `json:"return_url,omitempty"`
}

// PaymentResponse represents a payment response
type PaymentResponse struct {
	ID            string     `json:"id"`
	PaymentURL    string     `json:"payment_url,omitempty"`    // For traditional payments
	WalletAddress string     `json:"wallet_address,omitempty"` // For crypto payments
	Amount        float64    `json:"amount"`
	Currency      string     `json:"currency"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Status        string     `json:"status"`
	QRCode        string     `json:"qr_code,omitempty"` // For crypto payments
}

// UsageReportRequest represents a usage report from analytics
type UsageReportRequest struct {
	TenantID          string  `json:"tenant_id" binding:"required"`
	ClusterID         string  `json:"cluster_id" binding:"required"`
	StreamHours       float64 `json:"stream_hours"`
	EgressGB          float64 `json:"egress_gb"`
	RecordingGB       float64 `json:"recording_gb"`
	PeakBandwidthMbps float64 `json:"peak_bandwidth_mbps"`
	BillingMonth      string  `json:"billing_month" binding:"required"`
	UsageDetails      JSONB   `json:"usage_details,omitempty"`
}

// === PERISCOPE SERVICE TYPES ===

// UsageSummary represents usage summary for billing
type UsageSummary struct {
	TenantID          string    `json:"tenant_id"`
	ClusterID         string    `json:"cluster_id"`
	Period            string    `json:"period"`
	StreamHours       float64   `json:"stream_hours"`
	EgressGB          float64   `json:"egress_gb"`
	RecordingGB       float64   `json:"recording_gb"`
	PeakBandwidthMbps float64   `json:"peak_bandwidth_mbps"`
	TotalStreams      int       `json:"total_streams"`
	TotalViewers      int       `json:"total_viewers"`
	PeakViewers       int       `json:"peak_viewers"`
	MaxViewers        int       `json:"max_viewers"`
	UniqueUsers       int       `json:"unique_users"`
	BillingMonth      string    `json:"billing_month"`
	Timestamp         time.Time `json:"timestamp"`

	// Additional metrics from ClickHouse
	AvgViewers      float64 `json:"avg_viewers"`
	UniqueCountries int     `json:"unique_countries"`
	UniqueCities    int     `json:"unique_cities"`
	AvgBufferHealth float32 `json:"avg_buffer_health"`
	AvgBitrate      int     `json:"avg_bitrate"`
	PacketLossRate  float32 `json:"packet_loss_rate"`
}

// === COMMODORE SERVICE TYPES ===

// TenantFeatures represents enabled features for a tenant
type TenantFeatures struct {
	IsRecordingEnabled   bool `json:"is_recording_enabled"`
	IsAnalyticsEnabled   bool `json:"is_analytics_enabled"`
	IsAPIEnabled         bool `json:"is_api_enabled"`
	IsWhiteLabelEnabled  bool `json:"is_white_label_enabled"`
	IsRealtimeEnabled    bool `json:"is_realtime_enabled"`
	IsClipEnabled        bool `json:"is_clip_enabled"`
	IsMultistreamEnabled bool `json:"is_multistream_enabled"`
	IsTranscodingEnabled bool `json:"is_transcoding_enabled"`
	IsDVREnabled         bool `json:"is_dvr_enabled"`
	IsGeoBlockingEnabled bool `json:"is_geo_blocking_enabled"`
}

// TenantLimits represents resource limits for a tenant
type TenantLimits struct {
	MaxStreams         int `json:"max_streams"`
	MaxStorageGB       int `json:"max_storage_gb"`
	MaxBandwidthGB     int `json:"max_bandwidth_gb"`
	MaxUsers           int `json:"max_users"`
	MaxBitrateMbps     int `json:"max_bitrate_mbps"`
	MaxResolution      int `json:"max_resolution"`
	MaxRecordingHours  int `json:"max_recording_hours"`
	MaxAPICallsPerHour int `json:"max_api_calls_per_hour"`
}

// TenantValidation represents tenant validation result
type TenantValidation struct {
	IsValid  bool           `json:"is_valid"`
	Tenant   Tenant         `json:"tenant,omitempty"`
	Features TenantFeatures `json:"features,omitempty"`
	Limits   TenantLimits   `json:"limits,omitempty"`
	Message  string         `json:"message,omitempty"`
}

// ValidateTenantRequest represents a request to validate a tenant
type ValidateTenantRequest struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id,omitempty"`
}

// ValidateTenantResponse represents a tenant validation response
type ValidateTenantResponse struct {
	Valid    bool   `json:"valid"`
	Name     string `json:"name"`
	IsActive bool   `json:"is_active"`
	Error    string `json:"error,omitempty"`
}

// ResolveTenantRequest represents a tenant resolution request
type ResolveTenantRequest struct {
	Subdomain string `json:"subdomain,omitempty"`
	Domain    string `json:"domain,omitempty"`
}

// ResolveTenantResponse represents a tenant resolution response
type ResolveTenantResponse struct {
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	Error    string `json:"error,omitempty"`
}

// StreamValidationResponse represents stream validation response
type StreamValidationResponse struct {
	Valid        bool   `json:"valid"`
	StreamID     string `json:"stream_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	TenantID     string `json:"tenant_id,omitempty"`
	InternalName string `json:"internal_name,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ClipRequest represents a request to create a stream clip
type ClipRequest struct {
	StreamID  string `json:"stream_id" binding:"required"`
	StartTime int64  `json:"start_time" binding:"required"` // milliseconds from stream start
	Duration  int64  `json:"duration" binding:"required"`   // duration in milliseconds
	Title     string `json:"title"`
}

// StreamRequest represents a request for stream routing
type StreamRequest struct {
	TenantID string `json:"tenant_id"`
	StreamID string `json:"stream_id"`
}

// CreateStreamRequest represents a stream creation request
type CreateStreamRequest struct {
	Title       string `json:"title" binding:"required"`
	Description string `json:"description"`
	IsPublic    bool   `json:"is_public"`
	IsRecording bool   `json:"is_recording"`
	MaxViewers  int    `json:"max_viewers"`
}
