package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// JSONB is a custom type for handling JSONB fields
type JSONB map[string]interface{}

// Value implements the driver.Valuer interface for JSONB
func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

// Scan implements the sql.Scanner interface for JSONB
func (j *JSONB) Scan(value interface{}) error {
	if value == nil {
		*j = nil
		return nil
	}

	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return nil
	}

	return json.Unmarshal(bytes, j)
}

// BillingTier represents a billing tier with complex feature matrices
type BillingTier struct {
	ID          string `json:"id" db:"id"`
	TierName    string `json:"tier_name" db:"tier_name"`
	DisplayName string `json:"display_name" db:"display_name"`
	Description string `json:"description" db:"description"`

	// Pricing structure
	BasePrice     float64 `json:"base_price" db:"base_price"`
	Currency      string  `json:"currency" db:"currency"`
	BillingPeriod string  `json:"billing_period" db:"billing_period"`

	// Resource allocations per tier
	BandwidthAllocation JSONB `json:"bandwidth_allocation" db:"bandwidth_allocation"`
	StorageAllocation   JSONB `json:"storage_allocation" db:"storage_allocation"`
	ComputeAllocation   JSONB `json:"compute_allocation" db:"compute_allocation"`

	// Feature matrix
	Features JSONB `json:"features" db:"features"`

	// Service levels
	SupportLevel string `json:"support_level" db:"support_level"`
	SLALevel     string `json:"sla_level" db:"sla_level"`

	// Metering configuration
	MeteringEnabled bool  `json:"metering_enabled" db:"metering_enabled"`
	OverageRates    JSONB `json:"overage_rates" db:"overage_rates"`

	// Tier metadata
	IsActive     bool `json:"is_active" db:"is_active"`
	SortOrder    int  `json:"sort_order" db:"sort_order"`
	IsEnterprise bool `json:"is_enterprise" db:"is_enterprise"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// TenantSubscription represents a tenant's billing subscription
type TenantSubscription struct {
	ID       string `json:"id" db:"id"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	TierID   string `json:"tier_id" db:"tier_id"`

	// Subscription details
	Status       string `json:"status" db:"status"`
	BillingEmail string `json:"billing_email" db:"billing_email"`

	// Subscription period
	StartedAt       time.Time  `json:"started_at" db:"started_at"`
	TrialEndsAt     *time.Time `json:"trial_ends_at,omitempty" db:"trial_ends_at"`
	NextBillingDate *time.Time `json:"next_billing_date,omitempty" db:"next_billing_date"`
	CancelledAt     *time.Time `json:"cancelled_at,omitempty" db:"cancelled_at"`

	// Custom arrangements (for enterprise tiers)
	CustomPricing     JSONB `json:"custom_pricing" db:"custom_pricing"`
	CustomFeatures    JSONB `json:"custom_features" db:"custom_features"`
	CustomAllocations JSONB `json:"custom_allocations" db:"custom_allocations"`

	// Payment info
	PaymentMethod    *string `json:"payment_method,omitempty" db:"payment_method"`
	PaymentReference *string `json:"payment_reference,omitempty" db:"payment_reference"`

	// Billing address and tax
	BillingAddress JSONB    `json:"billing_address" db:"billing_address"`
	TaxID          *string  `json:"tax_id,omitempty" db:"tax_id"`
	TaxRate        *float64 `json:"tax_rate,omitempty" db:"tax_rate"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// UsageRecord represents tenant usage on a specific cluster
type UsageRecord struct {
	ID           string    `json:"id" db:"id"`
	TenantID     string    `json:"tenant_id" db:"tenant_id"`
	ClusterID    string    `json:"cluster_id" db:"cluster_id"`
	UsageType    string    `json:"usage_type" db:"usage_type"`
	UsageValue   float64   `json:"usage_value" db:"usage_value"`
	UsageDetails JSONB     `json:"usage_details" db:"usage_details"`
	BillingMonth string    `json:"billing_month" db:"billing_month"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
}

// Invoice represents a billing invoice
type Invoice struct {
	ID            string     `json:"id" db:"id"`
	TenantID      string     `json:"tenant_id" db:"tenant_id"`
	Amount        float64    `json:"amount" db:"amount"`
	BaseAmount    float64    `json:"base_amount" db:"base_amount"`
	MeteredAmount float64    `json:"metered_amount" db:"metered_amount"`
	Currency      string     `json:"currency" db:"currency"`
	Status        string     `json:"status" db:"status"` // pending, paid, failed, cancelled
	DueDate       time.Time  `json:"due_date" db:"due_date"`
	PaidAt        *time.Time `json:"paid_at" db:"paid_at"`
	UsageDetails  JSONB      `json:"usage_details" db:"usage_details"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

// Payment represents a payment transaction
type Payment struct {
	ID          string     `json:"id" db:"id"`
	InvoiceID   string     `json:"invoice_id" db:"invoice_id"`
	Method      string     `json:"method" db:"method"` // mollie, crypto_btc, crypto_eth, etc.
	Amount      float64    `json:"amount" db:"amount"`
	Currency    string     `json:"currency" db:"currency"`
	TxID        string     `json:"tx_id" db:"tx_id"`   // Transaction ID from payment provider
	Status      string     `json:"status" db:"status"` // pending, confirmed, failed
	ConfirmedAt *time.Time `json:"confirmed_at" db:"confirmed_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`
}

// CryptoWallet represents a crypto payment wallet
type CryptoWallet struct {
	ID            string    `json:"id" db:"id"`
	TenantID      string    `json:"tenant_id" db:"tenant_id"`
	InvoiceID     string    `json:"invoice_id" db:"invoice_id"`
	Asset         string    `json:"asset" db:"asset"` // BTC, ETH, USDC, LPT
	WalletAddress string    `json:"wallet_address" db:"wallet_address"`
	Status        string    `json:"status" db:"status"` // active, used, expired
	ExpiresAt     time.Time `json:"expires_at" db:"expires_at"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

// UsageIngestRequest represents the request payload from Periscope to Purser
// for usage ingestion.
type UsageIngestRequest struct {
	UsageSummaries []UsageSummary `json:"usage_summaries" binding:"required"`
	Source         string         `json:"source"`
	Timestamp      time.Time      `json:"timestamp"`
}
