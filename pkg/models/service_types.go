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
}

// === PERISCOPE SERVICE TYPES ===

// CountryMetrics represents viewer metrics for a single country
type CountryMetrics struct {
	CountryCode string  `json:"country_code"`
	ViewerCount int     `json:"viewer_count"`
	ViewerHours float64 `json:"viewer_hours"`
	EgressGB    float64 `json:"egress_gb"`
	Percentage  float64 `json:"percentage"`
}

// APIUsageBreakdown represents API usage aggregates by auth and operation type.
type APIUsageBreakdown struct {
	AuthType        string  `json:"auth_type"`
	OperationType   string  `json:"operation_type"`
	OperationName   string  `json:"operation_name,omitempty"`
	Service         string  `json:"service,omitempty"`
	LLMModel        string  `json:"llm_model,omitempty"`
	LLMProvider     string  `json:"llm_provider,omitempty"`
	Requests        float64 `json:"requests"`
	Errors          float64 `json:"errors"`
	DurationMs      float64 `json:"duration_ms"`
	Complexity      float64 `json:"complexity"`
	LLMInputTokens  float64 `json:"llm_input_tokens,omitempty"`
	LLMOutputTokens float64 `json:"llm_output_tokens,omitempty"`
	UniqueUsers     float64 `json:"unique_users,omitempty"`
	UniqueTokens    float64 `json:"unique_tokens,omitempty"`
}

// MeterQuantity is one canonical, dimensioned quantity in a regional billing
// report. Tenant, cluster, stream, and operation identity belong on the
// envelope or source fact and must never be dimensions.
type MeterQuantity struct {
	Meter      string  `json:"meter"`
	Unit       string  `json:"unit"`
	Quantity   float64 `json:"quantity"`
	Dimensions JSONB   `json:"dimensions,omitempty"`
}

// ProviderUsage attributes a customer-facing quantity to the infrastructure
// provider that performed the work. Delivery, processing, and storage share
// this settlement input.
type ProviderUsage struct {
	ProviderTenantID  string        `json:"provider_tenant_id,omitempty"`
	ProviderClusterID string        `json:"provider_cluster_id,omitempty"`
	Meter             MeterQuantity `json:"meter"`
}

// UsageAdjustment carries an additive metering correction. The original
// usage_records row is immutable; rating applies these deltas alongside
// canonical delta rows.
type UsageAdjustment struct {
	SourceSystem string    `json:"source_system"`
	SourceID     string    `json:"source_id"`
	UsageType    string    `json:"usage_type"`
	Unit         string    `json:"unit,omitempty"`
	Dimensions   JSONB     `json:"dimensions,omitempty"`
	ClusterID    string    `json:"cluster_id,omitempty"`
	DeltaValue   float64   `json:"delta_value"`
	PeriodStart  time.Time `json:"period_start"`
	PeriodEnd    time.Time `json:"period_end"`
	Reason       string    `json:"reason,omitempty"`
	Details      JSONB     `json:"details,omitempty"`
}

// UsageSummary is the dimension-aware billing envelope emitted by the regional
// Periscope metering worker and consumed by Purser. Source services emit
// operational facts, never this financial summary.
type UsageSummary struct {
	ReportID     string    `json:"report_id"`
	ReportKind   string    `json:"report_kind"` // finalized | reservation | window_complete
	SourceID     string    `json:"source_id"`
	SourceRegion string    `json:"source_region,omitempty"`
	Sequence     uint64    `json:"sequence"`
	TenantID     string    `json:"tenant_id"`
	ClusterID    string    `json:"cluster_id"` // work-performing cluster
	PeriodStart  time.Time `json:"period_start"`
	PeriodEnd    time.Time `json:"period_end"`
	Complete     bool      `json:"complete"`

	Meters           []MeterQuantity   `json:"meters"`
	ProviderUsage    []ProviderUsage   `json:"provider_usage,omitempty"`
	UsageAdjustments []UsageAdjustment `json:"usage_adjustments,omitempty"`
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
