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
	UsageSummary    JSONB              `json:"usage_summary,omitempty"`
}

// === PERISCOPE SERVICE TYPES ===

// CountryMetrics represents viewer metrics for a single country
// Used in geo_breakdown for rich billing/email summaries
type CountryMetrics struct {
	CountryCode string  `json:"country_code"`
	ViewerCount int     `json:"viewer_count"`
	ViewerHours float64 `json:"viewer_hours"`
	Percentage  float64 `json:"percentage"` // Percentage of total viewers
	EgressGB    float64 `json:"egress_gb"`
}

// UsageSummary represents usage summary for billing
type UsageSummary struct {
	TenantID          string  `json:"tenant_id"`
	ClusterID         string  `json:"cluster_id"`
	Period            string  `json:"period"`
	StreamHours       float64 `json:"stream_hours"`
	EgressGB          float64 `json:"egress_gb"`
	RecordingGB       float64 `json:"recording_gb"`
	PeakBandwidthMbps float64 `json:"peak_bandwidth_mbps"`
	// Storage and clip lifecycle metrics for billing
	StorageGB            float64   `json:"storage_gb"`
	AverageStorageGB     float64   `json:"average_storage_gb"`
	ClipsAdded           int       `json:"clips_added"`
	ClipsDeleted         int       `json:"clips_deleted"`
	ClipStorageAddedGB   float64   `json:"clip_storage_added_gb"`
	ClipStorageDeletedGB float64   `json:"clip_storage_deleted_gb"`
	TotalStreams         int       `json:"total_streams"`
	TotalViewers         int       `json:"total_viewers"`
	ViewerHours          float64   `json:"viewer_hours"`
	PeakViewers          int       `json:"peak_viewers"`
	MaxViewers           int       `json:"max_viewers"`
	UniqueUsers          int       `json:"unique_users"`
	BillingMonth         string    `json:"billing_month"`
	Timestamp            time.Time `json:"timestamp"`

	// Additional metrics from ClickHouse
	AvgViewers      float64          `json:"avg_viewers"`
	UniqueCountries int              `json:"unique_countries"`
	UniqueCities    int              `json:"unique_cities"`
	GeoBreakdown    []CountryMetrics `json:"geo_breakdown"` // Rich geo breakdown with viewers, hours, percentage
	AvgBufferHealth float32          `json:"avg_buffer_health"`
	AvgBitrate      int              `json:"avg_bitrate"`
	PacketLossRate  float32          `json:"packet_loss_rate"`
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
