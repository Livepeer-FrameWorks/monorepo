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
	AuthType      string  `json:"auth_type"`
	OperationType string  `json:"operation_type"`
	OperationName string  `json:"operation_name,omitempty"`
	Requests      float64 `json:"requests"`
	Errors        float64 `json:"errors"`
	DurationMs    float64 `json:"duration_ms"`
	Complexity    float64 `json:"complexity"`
	UniqueUsers   float64 `json:"unique_users,omitempty"`
	UniqueTokens  float64 `json:"unique_tokens,omitempty"`
}

// StorageProviderUsage is the provider-keyed storage settlement input.
// Customer invoices aggregate by serving cluster/scope; this record preserves
// the capacity owner and backend so marketplace settlement can be computed
// from the same metering pass.
type StorageProviderUsage struct {
	CustomerClusterID        string  `json:"customer_cluster_id,omitempty"`
	StorageProviderTenantID  string  `json:"storage_provider_tenant_id,omitempty"`
	StorageProviderClusterID string  `json:"storage_provider_cluster_id,omitempty"`
	StorageBackend           string  `json:"storage_backend,omitempty"`
	StorageScope             string  `json:"storage_scope"`
	UsageType                string  `json:"usage_type"`
	GBSeconds                float64 `json:"gb_seconds"`
}

// UsageAdjustment carries an additive metering correction. The original
// usage_records row is immutable; rating applies these deltas alongside
// canonical delta rows.
type UsageAdjustment struct {
	SourceSystem string    `json:"source_system"`
	SourceID     string    `json:"source_id"`
	UsageType    string    `json:"usage_type"`
	ClusterID    string    `json:"cluster_id,omitempty"`
	DeltaValue   float64   `json:"delta_value"`
	PeriodStart  time.Time `json:"period_start"`
	PeriodEnd    time.Time `json:"period_end"`
	Reason       string    `json:"reason,omitempty"`
	Details      JSONB     `json:"details,omitempty"`
}

// UsageSummary is the per-cluster billing envelope emitted by Periscope and
// consumed by Purser. Region fields are producer attribution hints for
// display/audit; rating resolves commercial pricing from ClusterID.
type UsageSummary struct {
	TenantID              string  `json:"tenant_id"`
	ClusterID             string  `json:"cluster_id"`
	SourceRegion          string  `json:"source_region,omitempty"`
	StreamOriginRegion    string  `json:"stream_origin_region,omitempty"`
	StreamOriginClusterID string  `json:"stream_origin_cluster_id,omitempty"`
	Period                string  `json:"period"`
	StreamHours           float64 `json:"stream_hours"`
	IngressGB             float64 `json:"ingress_gb,omitempty"`
	EgressGB              float64 `json:"egress_gb"`
	PeakBandwidthMbps     float64 `json:"peak_bandwidth_mbps"`
	DisplayStorageGB      float64 `json:"display_storage_gb"`                // display-only average storage over the period
	StorageGBSecondsHot   float64 `json:"storage_gb_seconds_hot,omitempty"`  // edge cache (operational, see meter-contracts.md)
	StorageGBSecondsCold  float64 `json:"storage_gb_seconds_cold,omitempty"` // S3 (rated default)

	// Per-codec breakdown: Livepeer (external gateway)
	LivepeerH264Seconds float64 `json:"livepeer_h264_seconds"`
	LivepeerVP9Seconds  float64 `json:"livepeer_vp9_seconds"`
	LivepeerAV1Seconds  float64 `json:"livepeer_av1_seconds"`
	LivepeerHEVCSeconds float64 `json:"livepeer_hevc_seconds"`

	// Per-codec breakdown: Native AV (local processing)
	NativeAvH264Seconds float64            `json:"native_av_h264_seconds"`
	NativeAvVP9Seconds  float64            `json:"native_av_vp9_seconds"`
	NativeAvAV1Seconds  float64            `json:"native_av_av1_seconds"`
	NativeAvHEVCSeconds float64            `json:"native_av_hevc_seconds"`
	NativeAvAACSeconds  float64            `json:"native_av_aac_seconds"`
	NativeAvOpusSeconds float64            `json:"native_av_opus_seconds"`
	ProcessingSeconds   map[string]float64 `json:"processing_seconds,omitempty"` // process:codec -> input media seconds

	// Viewer metrics
	TotalStreams int       `json:"total_streams"`
	TotalViewers int       `json:"total_viewers"`
	ViewerHours  float64   `json:"viewer_hours"`
	MaxViewers   int       `json:"max_viewers"`
	UniqueUsers  int       `json:"unique_users"`
	Timestamp    time.Time `json:"timestamp"`

	// API usage aggregates; unrated by default, but custom pricing can opt in.
	APIRequests   float64             `json:"api_requests"`
	APIErrors     float64             `json:"api_errors"`
	APIDurationMs float64             `json:"api_duration_ms"`
	APIComplexity float64             `json:"api_complexity"`
	APIBreakdown  []APIUsageBreakdown `json:"api_breakdown,omitempty"`

	Meters               map[string]float64     `json:"meters,omitempty"`
	StorageProviderUsage []StorageProviderUsage `json:"storage_provider_usage,omitempty"`
	UsageAdjustments     []UsageAdjustment      `json:"usage_adjustments,omitempty"`
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
