package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"
)

// BillingFeatures represents the feature flags available in a billing tier.
// NOTE: Enforcement limits (max_streams, max_viewers, bandwidth caps) belong
// in quartermaster.tenant_cluster_assignments, not here. This is billing only.
type BillingFeatures struct {
	Recording              bool   `json:"recording"`
	Analytics              bool   `json:"analytics"`
	CustomBranding         bool   `json:"custom_branding,omitempty"`
	APIAccess              bool   `json:"api_access,omitempty"`
	SupportLevel           string `json:"support_level"`
	SLA                    bool   `json:"sla,omitempty"`
	ProcessingCustomizable bool   `json:"processing_customizable,omitempty"`
}

// PricingRule mirrors purser.tier_pricing_rules and the rating engine's Rule
// type. Decimal fields are strings to preserve precision across JSON / gRPC.
type PricingRule struct {
	Meter            string         `json:"meter"`
	Model            string         `json:"model"`
	Currency         string         `json:"currency"`
	IncludedQuantity string         `json:"included_quantity"`
	UnitPrice        string         `json:"unit_price"`
	Config           map[string]any `json:"config,omitempty"`
}

// Entitlement is one (key, value) row from purser.tier_entitlements or
// purser.subscription_entitlement_overrides. Value is a JSON-encoded scalar
// (bare integer, string, or boolean — never a wrapper object).
type Entitlement struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// UsageDetail represents a single usage line item.
type UsageDetail struct {
	Quantity  float64 `json:"quantity"`
	UnitPrice float64 `json:"unit_price"`
	Unit      string  `json:"unit,omitempty"`
}

// UsageDetails represents all usage details for an invoice.
type UsageDetails map[string]UsageDetail

// BillingAddress represents a billing address.
type BillingAddress struct {
	Street     string `json:"street"`
	City       string `json:"city"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

// JSONB is a generic typed JSONB map.
type JSONB map[string]any

func (j JSONB) Value() (driver.Value, error) {
	if j == nil {
		return nil, nil
	}
	return json.Marshal(j)
}

func (j *JSONB) Scan(value any) error {
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

func valueFromJSON(v any) (driver.Value, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

func scanToJSON(dest any, value any) error {
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

func (bf BillingFeatures) Value() (driver.Value, error) { return valueFromJSON(bf) }
func (bf *BillingFeatures) Scan(value any) error        { return scanToJSON(bf, value) }

func (ud UsageDetails) Value() (driver.Value, error) { return valueFromJSON(ud) }
func (ud *UsageDetails) Scan(value any) error        { return scanToJSON(ud, value) }

func (ba BillingAddress) Value() (driver.Value, error) { return valueFromJSON(ba) }
func (ba *BillingAddress) Scan(value any) error        { return scanToJSON(ba, value) }

// BillingTier represents a billing tier row joined with its pricing rules and
// entitlements. PricingRules and Entitlements are loaded by the application
// layer from purser.tier_pricing_rules and purser.tier_entitlements.
type BillingTier struct {
	ID          string `json:"id" db:"id"`
	TierName    string `json:"tier_name" db:"tier_name"`
	DisplayName string `json:"display_name" db:"display_name"`
	Description string `json:"description" db:"description"`

	BasePrice     float64 `json:"base_price" db:"base_price"`
	Currency      string  `json:"currency" db:"currency"`
	BillingPeriod string  `json:"billing_period" db:"billing_period"`

	Features BillingFeatures `json:"features" db:"features"`

	SupportLevel string `json:"support_level" db:"support_level"`
	SLALevel     string `json:"sla_level" db:"sla_level"`

	MeteringEnabled bool          `json:"metering_enabled" db:"metering_enabled"`
	PricingRules    []PricingRule `json:"pricing_rules"`
	Entitlements    []Entitlement `json:"entitlements"`

	IsActive     bool `json:"is_active" db:"is_active"`
	TierLevel    int  `json:"tier_level" db:"tier_level"`
	IsEnterprise bool `json:"is_enterprise" db:"is_enterprise"`

	IsDefaultPrepaid  bool `json:"is_default_prepaid" db:"is_default_prepaid"`
	IsDefaultPostpaid bool `json:"is_default_postpaid" db:"is_default_postpaid"`

	ProcessesLive json.RawMessage `json:"processes_live" db:"processes_live"`
	ProcessesVod  json.RawMessage `json:"processes_vod" db:"processes_vod"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// TenantSubscription represents a tenant's billing subscription. Per-tenant
// pricing/entitlement overrides live in purser.subscription_pricing_overrides
// and purser.subscription_entitlement_overrides; PricingOverrides /
// EntitlementOverrides are loaded by the application layer.
type TenantSubscription struct {
	ID       string `json:"id" db:"id"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	TierID   string `json:"tier_id" db:"tier_id"`

	Status       string `json:"status" db:"status"`
	BillingEmail string `json:"billing_email" db:"billing_email"`

	StartedAt          time.Time  `json:"started_at" db:"started_at"`
	TrialEndsAt        *time.Time `json:"trial_ends_at,omitempty" db:"trial_ends_at"`
	NextBillingDate    *time.Time `json:"next_billing_date,omitempty" db:"next_billing_date"`
	BillingPeriodStart *time.Time `json:"billing_period_start,omitempty" db:"billing_period_start"`
	BillingPeriodEnd   *time.Time `json:"billing_period_end,omitempty" db:"billing_period_end"`
	CancelledAt        *time.Time `json:"cancelled_at,omitempty" db:"cancelled_at"`

	CustomFeatures       BillingFeatures `json:"custom_features" db:"custom_features"`
	PricingOverrides     []PricingRule   `json:"pricing_overrides"`
	EntitlementOverrides []Entitlement   `json:"entitlement_overrides"`

	PaymentMethod    *string `json:"payment_method,omitempty" db:"payment_method"`
	PaymentReference *string `json:"payment_reference,omitempty" db:"payment_reference"`

	BillingAddress BillingAddress `json:"billing_address" db:"billing_address"`
	TaxID          *string        `json:"tax_id,omitempty" db:"tax_id"`
	TaxRate        *float64       `json:"tax_rate,omitempty" db:"tax_rate"`

	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// UsageRecord represents tenant usage on a specific cluster.
type UsageRecord struct {
	ID           string       `json:"id" db:"id"`
	TenantID     string       `json:"tenant_id" db:"tenant_id"`
	ClusterID    string       `json:"cluster_id" db:"cluster_id"`
	UsageType    string       `json:"usage_type" db:"usage_type"`
	UsageValue   float64      `json:"usage_value" db:"usage_value"`
	UsageDetails UsageDetails `json:"usage_details" db:"usage_details"`
	CreatedAt    time.Time    `json:"created_at" db:"created_at"`
}

// Invoice represents a billing invoice. Detailed line items live in
// purser.invoice_line_items and are loaded separately by handlers.
type Invoice struct {
	ID            string       `json:"id" db:"id"`
	TenantID      string       `json:"tenant_id" db:"tenant_id"`
	Amount        float64      `json:"amount" db:"amount"`
	BaseAmount    float64      `json:"base_amount" db:"base_amount"`
	MeteredAmount float64      `json:"metered_amount" db:"metered_amount"`
	Currency      string       `json:"currency" db:"currency"`
	Status        string       `json:"status" db:"status"`
	DueDate       time.Time    `json:"due_date" db:"due_date"`
	PaidAt        *time.Time   `json:"paid_at" db:"paid_at"`
	UsageDetails  UsageDetails `json:"usage_details" db:"usage_details"`
	CreatedAt     time.Time    `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at" db:"updated_at"`
}

// Payment represents a payment transaction.
type Payment struct {
	ID          string     `json:"id" db:"id"`
	InvoiceID   string     `json:"invoice_id" db:"invoice_id"`
	Method      string     `json:"method" db:"method"`
	Amount      float64    `json:"amount" db:"amount"`
	Currency    string     `json:"currency" db:"currency"`
	TxID        string     `json:"tx_id" db:"tx_id"`
	Status      string     `json:"status" db:"status"`
	ConfirmedAt *time.Time `json:"confirmed_at" db:"confirmed_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`
}

// GraphQL union type marker method for Payment
func (Payment) IsCreatePaymentResult() {}

// CryptoWallet represents a crypto payment wallet.
type CryptoWallet struct {
	ID            string    `json:"id" db:"id"`
	TenantID      string    `json:"tenant_id" db:"tenant_id"`
	InvoiceID     string    `json:"invoice_id" db:"invoice_id"`
	Asset         string    `json:"asset" db:"asset"`
	WalletAddress string    `json:"wallet_address" db:"wallet_address"`
	Status        string    `json:"status" db:"status"`
	ExpiresAt     time.Time `json:"expires_at" db:"expires_at"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

// UsageIngestRequest is the request payload from Periscope to Purser for usage
// ingestion.
type UsageIngestRequest struct {
	UsageSummaries []UsageSummary `json:"usage_summaries" binding:"required"`
	Source         string         `json:"source"`
	Timestamp      time.Time      `json:"timestamp"`
}
