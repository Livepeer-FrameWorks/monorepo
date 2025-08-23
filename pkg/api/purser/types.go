package purser

import (
	"time"

	"frameworks/pkg/api/common"
	"frameworks/pkg/models"
)

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

// ErrorResponse is a type alias to the common error response
type ErrorResponse = common.ErrorResponse

// PaymentMethodErrorResponse represents an error with available payment methods
type PaymentMethodErrorResponse struct {
	Error            string   `json:"error"`
	AvailableMethods []string `json:"available_methods"`
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

// GetBillingTiersResponse represents the response from get billing tiers API
type GetBillingTiersResponse struct {
	Tiers          []models.BillingTier `json:"tiers"`
	Count          int                  `json:"count"`
	PaymentMethods []string             `json:"payment_methods"`
}

// GetInvoiceResponse represents a single invoice response
type GetInvoiceResponse struct {
	Invoice models.Invoice     `json:"invoice"`
	Tier    models.BillingTier `json:"tier"`
}

type InvoiceLineItem struct {
	Description string  `json:"description"`
	Amount      float64 `json:"amount"`
	Quantity    int     `json:"quantity"`
}

// GetInvoicesResponse represents a list of invoices response
type GetInvoicesResponse struct {
	Invoices []models.Invoice `json:"invoices"`
	Total    int              `json:"total"`
	Limit    int              `json:"limit"`
	Offset   int              `json:"offset"`
}

// CreatePaymentResponse represents the response from creating a payment
type CreatePaymentResponse struct {
	PaymentID  string    `json:"payment_id"`
	PaymentURL string    `json:"payment_url"`
	Status     string    `json:"status"`
	Amount     float64   `json:"amount"`
	Currency   string    `json:"currency"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// UpdateBillingTierResponse represents the response from updating billing tier
type UpdateBillingTierResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// PaymentMethodResponse represents available payment methods
type PaymentMethodResponse struct {
	Methods []string `json:"methods"`
}

// BillingStatusResponse represents billing status for a tenant
type BillingStatusResponse struct {
	TenantID          string    `json:"tenant_id"`
	CurrentTier       string    `json:"current_tier"`
	BillingStatus     string    `json:"billing_status"`
	NextBillingDate   time.Time `json:"next_billing_date"`
	OutstandingAmount float64   `json:"outstanding_amount"`
	Currency          string    `json:"currency"`
}

// === BILLING TIER MANAGEMENT (moved from pkg/models) ===

// CreateTierRequest represents a request to create a billing tier
type CreateTierRequest struct {
	TierName            string       `json:"tier_name" binding:"required"`
	DisplayName         string       `json:"display_name" binding:"required"`
	Description         string       `json:"description"`
	BasePrice           float64      `json:"base_price"`
	Currency            string       `json:"currency"`
	BillingPeriod       string       `json:"billing_period"`
	BandwidthAllocation models.JSONB `json:"bandwidth_allocation"`
	StorageAllocation   models.JSONB `json:"storage_allocation"`
	ComputeAllocation   models.JSONB `json:"compute_allocation"`
	Features            models.JSONB `json:"features"`
	SupportLevel        string       `json:"support_level"`
	SLALevel            string       `json:"sla_level"`
	MeteringEnabled     bool         `json:"metering_enabled"`
	OverageRates        models.JSONB `json:"overage_rates"`
	SortOrder           int          `json:"sort_order"`
	IsEnterprise        bool         `json:"is_enterprise"`
}

// UpdateTierRequest represents a request to update a billing tier
type UpdateTierRequest struct {
	DisplayName         *string      `json:"display_name,omitempty"`
	Description         *string      `json:"description,omitempty"`
	BasePrice           *float64     `json:"base_price,omitempty"`
	Currency            *string      `json:"currency,omitempty"`
	BillingPeriod       *string      `json:"billing_period,omitempty"`
	BandwidthAllocation models.JSONB `json:"bandwidth_allocation,omitempty"`
	StorageAllocation   models.JSONB `json:"storage_allocation,omitempty"`
	ComputeAllocation   models.JSONB `json:"compute_allocation,omitempty"`
	Features            models.JSONB `json:"features,omitempty"`
	SupportLevel        *string      `json:"support_level,omitempty"`
	SLALevel            *string      `json:"sla_level,omitempty"`
	MeteringEnabled     *bool        `json:"metering_enabled,omitempty"`
	OverageRates        models.JSONB `json:"overage_rates,omitempty"`
	SortOrder           *int         `json:"sort_order,omitempty"`
	IsEnterprise        *bool        `json:"is_enterprise,omitempty"`
	IsActive            *bool        `json:"is_active,omitempty"`
}

// === SUBSCRIPTION MANAGEMENT (moved from pkg/models) ===

// CreateSubscriptionRequest represents a request to create a subscription
type CreateSubscriptionRequest struct {
	TenantID          string       `json:"tenant_id" binding:"required"`
	TierID            string       `json:"tier_id" binding:"required"`
	BillingEmail      string       `json:"billing_email" binding:"required,email"`
	PaymentMethod     string       `json:"payment_method"`
	TrialEndsAt       *time.Time   `json:"trial_ends_at,omitempty"`
	CustomPricing     models.JSONB `json:"custom_pricing,omitempty"`
	CustomFeatures    models.JSONB `json:"custom_features,omitempty"`
	CustomAllocations models.JSONB `json:"custom_allocations,omitempty"`
}

// UpdateSubscriptionRequest represents a request to update a subscription
type UpdateSubscriptionRequest struct {
	TierID            *string      `json:"tier_id,omitempty"`
	BillingEmail      *string      `json:"billing_email,omitempty"`
	PaymentMethod     *string      `json:"payment_method,omitempty"`
	Status            *string      `json:"status,omitempty"`
	CustomPricing     models.JSONB `json:"custom_pricing,omitempty"`
	CustomFeatures    models.JSONB `json:"custom_features,omitempty"`
	CustomAllocations models.JSONB `json:"custom_allocations,omitempty"`
}

// === PAYMENT PROCESSING (moved from pkg/models) ===

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

// === USAGE REPORTING (moved from pkg/models) ===

// UsageReportRequest represents a usage report from analytics
type UsageReportRequest struct {
	TenantID          string       `json:"tenant_id" binding:"required"`
	ClusterID         string       `json:"cluster_id" binding:"required"`
	StreamHours       float64      `json:"stream_hours"`
	EgressGB          float64      `json:"egress_gb"`
	RecordingGB       float64      `json:"recording_gb"`
	PeakBandwidthMbps float64      `json:"peak_bandwidth_mbps"`
	BillingMonth      string       `json:"billing_month" binding:"required"`
	UsageDetails      models.JSONB `json:"usage_details,omitempty"`
}

// UsageRecord represents a single usage record for API responses
type UsageRecord struct {
	ID           string       `json:"id"`
	TenantID     string       `json:"tenant_id"`
	ClusterID    string       `json:"cluster_id"`
	ClusterName  *string      `json:"cluster_name"`
	UsageType    string       `json:"usage_type"`
	UsageValue   float64      `json:"usage_value"`
	UsageDetails models.JSONB `json:"usage_details"`
	BillingMonth string       `json:"billing_month"`
	CreatedAt    time.Time    `json:"created_at"`
}

// UsageRecordsResponse represents the response from GetUsageRecords
type UsageRecordsResponse struct {
	UsageRecords []UsageRecord `json:"usage_records"`
	TenantID     string        `json:"tenant_id"`
	Filters      UsageFilters  `json:"filters"`
}

// UsageFilters represents the filters applied to usage records
type UsageFilters struct {
	ClusterID    string `json:"cluster_id"`
	UsageType    string `json:"usage_type"`
	BillingMonth string `json:"billing_month"`
}

// UsageDetails represents structured usage details for JSONB storage
type UsageDetails struct {
	UsageData    map[string]float64 `json:"usage_data"`
	BillingMonth string             `json:"billing_month"`
	TierInfo     TierInfo           `json:"tier_info"`
}

// TierInfo represents tier information within usage details
type TierInfo struct {
	TierID          string  `json:"tier_id"`
	TierName        string  `json:"tier_name"`
	DisplayName     string  `json:"display_name"`
	BasePrice       float64 `json:"base_price"`
	MeteringEnabled bool    `json:"metering_enabled"`
}

// === MOLLIE PAYMENT INTEGRATION ===

// MollieWebhookPayload represents a webhook payload from Mollie
type MollieWebhookPayload struct {
	ID       string `json:"id"`
	Resource string `json:"resource"`
	Status   string `json:"status"`
}

// MolliePaymentRequest represents a payment request to Mollie API
type MolliePaymentRequest struct {
	Amount      MollieAmount      `json:"amount"`
	Description string            `json:"description"`
	RedirectURL string            `json:"redirectUrl"`
	WebhookURL  string            `json:"webhookUrl"`
	Metadata    map[string]string `json:"metadata"`
}

// MollieAmount represents amount structure for Mollie API
type MollieAmount struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

// MolliePaymentResponse represents a payment response from Mollie API
type MolliePaymentResponse struct {
	ID          string             `json:"id"`
	Status      string             `json:"status"`
	Amount      MollieAmount       `json:"amount"`
	Description string             `json:"description"`
	CreatedAt   time.Time          `json:"createdAt"`
	Links       MolliePaymentLinks `json:"_links"`
	Metadata    map[string]string  `json:"metadata"`
}

// MolliePaymentLinks represents the _links section of Mollie response
type MolliePaymentLinks struct {
	Self     MollieLink `json:"self"`
	Checkout MollieLink `json:"checkout"`
}

// MollieLink represents a single link in Mollie response
type MollieLink struct {
	Href string `json:"href"`
	Type string `json:"type"`
}

// === STRIPE PAYMENT INTEGRATION ===

// StripePaymentIntentResponse represents the response from Stripe's payment intent API
type StripePaymentIntentResponse struct {
	ID           string `json:"id"`
	ClientSecret string `json:"client_secret"`
	Status       string `json:"status"`
	Amount       int64  `json:"amount"`
	Currency     string `json:"currency"`
	Metadata     struct {
		InvoiceID string `json:"invoice_id"`
		TenantID  string `json:"tenant_id"`
	} `json:"metadata"`
}

// === BLOCKCYPHER BITCOIN INTEGRATION ===

// BlockCypherTransactionInput represents a transaction input for BlockCypher API
type BlockCypherTransactionInput struct {
	Addresses []string `json:"addresses"`
}

// BlockCypherTransactionOutput represents a transaction output for BlockCypher API
type BlockCypherTransactionOutput struct {
	Addresses []string `json:"addresses"`
	Value     int64    `json:"value"`
}

// BlockCypherTransactionRequest represents a transaction request to BlockCypher API
type BlockCypherTransactionRequest struct {
	Inputs      []BlockCypherTransactionInput  `json:"inputs"`
	Outputs     []BlockCypherTransactionOutput `json:"outputs"`
	PrivateKeys []string                       `json:"private_keys"`
}

// BlockCypherTransactionResponse represents a transaction response from BlockCypher API
type BlockCypherTransactionResponse struct {
	Hash        string `json:"hash"`
	Total       int64  `json:"total"`
	Fees        int64  `json:"fees"`
	Size        int    `json:"size"`
	Preference  string `json:"preference"`
	RelayedBy   string `json:"relayed_by"`
	Received    string `json:"received"`
	Confirmed   string `json:"confirmed"`
	DoubleSpend bool   `json:"double_spend"`
}
