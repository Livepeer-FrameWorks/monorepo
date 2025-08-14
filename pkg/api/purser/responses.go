package purser

import "frameworks/pkg/models"

// TenantTierInfoResponse represents the response from the tenant tier info API
type TenantTierInfoResponse = models.TenantTierInfo

// CheckUserLimitRequest represents a request to check user limits for a tenant
type CheckUserLimitRequest struct {
	TenantID string `json:"tenant_id"`
	Email    string `json:"email"`
}

// CheckUserLimitResponse represents the response from check user limit API
type CheckUserLimitResponse struct {
	Allowed      bool   `json:"allowed"`
	CurrentUsers int    `json:"current_users,omitempty"`
	MaxUsers     int    `json:"max_users,omitempty"`
	Error        string `json:"error,omitempty"`
}

// BillingDataRequest represents a request to submit billing/usage data
type BillingDataRequest struct {
	TenantID      string  `json:"tenant_id"`
	ResourceType  string  `json:"resource_type"` // bandwidth, storage, compute
	Usage         float64 `json:"usage"`
	Unit          string  `json:"unit"` // GB, hours, etc.
	BillingPeriod string  `json:"billing_period"`
	Timestamp     int64   `json:"timestamp"`
}

// BillingDataResponse represents the response from submitting billing data
type BillingDataResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

// TenantUsageRequest represents a request for tenant usage data
type TenantUsageRequest struct {
	TenantID  string `json:"tenant_id"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

// TenantUsageResponse represents usage data for a tenant
type TenantUsageResponse struct {
	TenantID      string             `json:"tenant_id"`
	BillingPeriod string             `json:"billing_period"`
	Usage         map[string]float64 `json:"usage"` // resource_type -> usage amount
	Costs         map[string]float64 `json:"costs"` // resource_type -> cost amount
	TotalCost     float64            `json:"total_cost"`
	Currency      string             `json:"currency"`
}

// SubscriptionInfo represents subscription details
type SubscriptionInfo struct {
	ID            string  `json:"id"`
	TenantID      string  `json:"tenant_id"`
	TierID        string  `json:"tier_id"`
	Status        string  `json:"status"`
	BillingPeriod string  `json:"billing_period"`
	StartDate     string  `json:"start_date"`
	EndDate       string  `json:"end_date,omitempty"`
	BasePrice     float64 `json:"base_price"`
	Currency      string  `json:"currency"`
}

// GetSubscriptionResponse represents the response from get subscription API
type GetSubscriptionResponse struct {
	Subscription *SubscriptionInfo `json:"subscription,omitempty"`
	Error        string            `json:"error,omitempty"`
}

// ErrorResponse represents a standard error response from Purser
type ErrorResponse struct {
	Error string `json:"error"`
}

// UsageIngestRequest represents a request to ingest usage summaries
type UsageIngestRequest struct {
	UsageSummaries []models.UsageSummary `json:"usage_summaries"`
	Source         string                `json:"source"`
	Timestamp      int64                 `json:"timestamp"`
}

// UsageIngestResponse represents the response from usage ingestion
type UsageIngestResponse struct {
	ProcessedCount int    `json:"processed_count"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
}
