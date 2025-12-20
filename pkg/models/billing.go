package models

import (
	"database/sql/driver"
	"encoding/json"
	"strings"
	"time"
)

// BillingFeatures represents the features available in a billing tier
// NOTE: Enforcement limits (max_streams, max_viewers, bandwidth caps) belong
// in quartermaster.tenant_cluster_assignments, not here. This is billing only.
type BillingFeatures struct {
	Recording      bool   `json:"recording"`
	Analytics      bool   `json:"analytics"`
	CustomBranding bool   `json:"custom_branding,omitempty"`
	APIAccess      bool   `json:"api_access,omitempty"`
	SupportLevel   string `json:"support_level"` // "community", "basic", "priority", "enterprise", "dedicated"
	SLA            bool   `json:"sla,omitempty"`
}

// AllocationDetails represents resource allocation for a billing tier
// Used everywhere: BillingFeatures, OverageRates, etc.
// Limit == nil means unlimited, otherwise it's the numeric limit
type AllocationDetails struct {
	Limit     *float64 `json:"limit"`
	UnitPrice float64  `json:"unit_price,omitempty"`
	Unit      string   `json:"unit,omitempty"`
}

// ProcessingRates represents transcoding/processing pricing rates
// H264RatePerMin is the base rate for H264 transcoding per minute
// CodecMultipliers are applied to the base rate for different codecs:
//   - H264: 1.0x (baseline)
//   - HEVC: 1.5x (more compute intensive)
//   - VP9:  1.5x (more compute intensive)
//   - AV1:  2.0x (most compute intensive)
//   - AAC/Opus/MP3: 0.0x (audio transcoding is free but tracked)
type ProcessingRates struct {
	H264RatePerMin   float64            `json:"h264_rate_per_min"`
	CodecMultipliers map[string]float64 `json:"codec_multipliers,omitempty"`
}

// GetCodecMultiplier returns the multiplier for a codec, defaulting to 1.0 for unknown codecs
func (p ProcessingRates) GetCodecMultiplier(codec string) float64 {
	if p.CodecMultipliers == nil {
		return 1.0
	}
	// Normalize codec name to lowercase
	codecLower := strings.ToLower(codec)
	if mult, ok := p.CodecMultipliers[codecLower]; ok {
		return mult
	}
	// Default to 1.0 for unknown video codecs, 0.0 for known audio codecs
	switch codecLower {
	case "aac", "opus", "mp3", "ac3", "flac", "vorbis":
		return 0.0
	default:
		return 1.0
	}
}

// OverageRates represents overage pricing rates
type OverageRates struct {
	Bandwidth  AllocationDetails `json:"bandwidth,omitempty"`
	Storage    AllocationDetails `json:"storage,omitempty"`
	Compute    AllocationDetails `json:"compute,omitempty"`
	Processing ProcessingRates   `json:"processing,omitempty"`
}

// UsageDetail represents a single usage line item
type UsageDetail struct {
	Quantity  float64 `json:"quantity"`
	UnitPrice float64 `json:"unit_price"`
	Unit      string  `json:"unit,omitempty"`
}

// UsageDetails represents all usage details for an invoice
type UsageDetails map[string]UsageDetail

// BillingAddress represents a billing address
type BillingAddress struct {
	Street     string `json:"street"`
	City       string `json:"city"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

// CustomPricing represents custom pricing arrangements
type CustomPricing struct {
	BasePrice    float64      `json:"base_price,omitempty"`
	DiscountRate float64      `json:"discount_rate,omitempty"`
	OverageRates OverageRates `json:"overage_rates,omitempty"`
}

// JSONB is a custom type for handling JSONB fields (keeping for backward compatibility)
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

// Generic JSON field implementation for typed structs
func valueFromJSON(v interface{}) (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func scanToJSON(dest interface{}, value interface{}) error {
	if value == nil {
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

	return json.Unmarshal(bytes, dest)
}

// Value/Scan implementations for typed structs
func (bf BillingFeatures) Value() (driver.Value, error) {
	return valueFromJSON(bf)
}

func (bf *BillingFeatures) Scan(value interface{}) error {
	return scanToJSON(bf, value)
}

func (ad AllocationDetails) Value() (driver.Value, error) {
	return valueFromJSON(ad)
}

func (ad *AllocationDetails) Scan(value interface{}) error {
	return scanToJSON(ad, value)
}

func (pr ProcessingRates) Value() (driver.Value, error) {
	return valueFromJSON(pr)
}

func (pr *ProcessingRates) Scan(value interface{}) error {
	return scanToJSON(pr, value)
}

func (or OverageRates) Value() (driver.Value, error) {
	return valueFromJSON(or)
}

func (or *OverageRates) Scan(value interface{}) error {
	return scanToJSON(or, value)
}

func (ud UsageDetails) Value() (driver.Value, error) {
	return valueFromJSON(ud)
}

func (ud *UsageDetails) Scan(value interface{}) error {
	return scanToJSON(ud, value)
}

func (ba BillingAddress) Value() (driver.Value, error) {
	return valueFromJSON(ba)
}

func (ba *BillingAddress) Scan(value interface{}) error {
	return scanToJSON(ba, value)
}

func (cp CustomPricing) Value() (driver.Value, error) {
	return valueFromJSON(cp)
}

func (cp *CustomPricing) Scan(value interface{}) error {
	return scanToJSON(cp, value)
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
	BandwidthAllocation AllocationDetails `json:"bandwidth_allocation" db:"bandwidth_allocation"`
	StorageAllocation   AllocationDetails `json:"storage_allocation" db:"storage_allocation"`
	ComputeAllocation   AllocationDetails `json:"compute_allocation" db:"compute_allocation"`

	// Feature matrix
	Features BillingFeatures `json:"features" db:"features"`

	// Service levels
	SupportLevel string `json:"support_level" db:"support_level"`
	SLALevel     string `json:"sla_level" db:"sla_level"`

	// Metering configuration
	MeteringEnabled bool         `json:"metering_enabled" db:"metering_enabled"`
	OverageRates    OverageRates `json:"overage_rates" db:"overage_rates"`

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
	CustomPricing     CustomPricing     `json:"custom_pricing" db:"custom_pricing"`
	CustomFeatures    BillingFeatures   `json:"custom_features" db:"custom_features"`
	CustomAllocations AllocationDetails `json:"custom_allocations" db:"custom_allocations"`

	// Payment info
	PaymentMethod    *string `json:"payment_method,omitempty" db:"payment_method"`
	PaymentReference *string `json:"payment_reference,omitempty" db:"payment_reference"`

	// Billing address and tax
	BillingAddress BillingAddress `json:"billing_address" db:"billing_address"`
	TaxID          *string        `json:"tax_id,omitempty" db:"tax_id"`
	TaxRate        *float64       `json:"tax_rate,omitempty" db:"tax_rate"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// UsageRecord represents tenant usage on a specific cluster
type UsageRecord struct {
	ID           string       `json:"id" db:"id"`
	TenantID     string       `json:"tenant_id" db:"tenant_id"`
	ClusterID    string       `json:"cluster_id" db:"cluster_id"`
	UsageType    string       `json:"usage_type" db:"usage_type"`
	UsageValue   float64      `json:"usage_value" db:"usage_value"`
	UsageDetails UsageDetails `json:"usage_details" db:"usage_details"`
	BillingMonth string       `json:"billing_month" db:"billing_month"`
	CreatedAt    time.Time    `json:"created_at" db:"created_at"`
}

// Invoice represents a billing invoice
type Invoice struct {
	ID            string       `json:"id" db:"id"`
	TenantID      string       `json:"tenant_id" db:"tenant_id"`
	Amount        float64      `json:"amount" db:"amount"`
	BaseAmount    float64      `json:"base_amount" db:"base_amount"`
	MeteredAmount float64      `json:"metered_amount" db:"metered_amount"`
	Currency      string       `json:"currency" db:"currency"`
	Status        string       `json:"status" db:"status"` // pending, paid, failed, cancelled
	DueDate       time.Time    `json:"due_date" db:"due_date"`
	PaidAt        *time.Time   `json:"paid_at" db:"paid_at"`
	UsageDetails  UsageDetails `json:"usage_details" db:"usage_details"`
	CreatedAt     time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at" db:"updated_at"`
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

// GraphQL union type marker method for Payment
func (Payment) IsCreatePaymentResult() {}

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
