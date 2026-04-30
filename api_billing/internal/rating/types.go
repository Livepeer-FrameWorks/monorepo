// Package rating is a pure billing engine: it turns metered usage and
// pricing rules into invoice line items. It has no DB, gRPC, or Stripe
// dependency — handlers load effective rules and usage, call Rate, and
// persist or return the result.
package rating

import (
	"time"

	"github.com/shopspring/decimal"
)

// Meter identifies a canonical metered quantity.
type Meter string

const (
	MeterDeliveredMinutes  Meter = "delivered_minutes"
	MeterAverageStorageGB  Meter = "average_storage_gb"
	MeterAIGPUHours        Meter = "ai_gpu_hours"
	MeterProcessingSeconds Meter = "processing_seconds"
)

// ValidMeter reports whether m is one of the canonical meters the rating engine
// understands.
func ValidMeter(m Meter) bool {
	switch m {
	case MeterDeliveredMinutes, MeterAverageStorageGB, MeterAIGPUHours, MeterProcessingSeconds:
		return true
	default:
		return false
	}
}

// Model identifies how a Rule converts usage to money.
type Model string

const (
	// ModelTieredGraduated bills (qty - included) * unit_price for usage above included.
	ModelTieredGraduated Model = "tiered_graduated"
	// ModelAllUsage bills every unit at unit_price.
	ModelAllUsage Model = "all_usage"
	// ModelCodecMultiplier bills processing seconds with per-codec multipliers.
	ModelCodecMultiplier Model = "codec_multiplier"
)

// ValidModel reports whether m is one of the pricing models Rate can execute.
func ValidModel(m Model) bool {
	switch m {
	case ModelTieredGraduated, ModelAllUsage, ModelCodecMultiplier:
		return true
	default:
		return false
	}
}

// Rule is one priced behavior for a meter.
type Rule struct {
	Meter            Meter
	Model            Model
	Currency         string
	IncludedQuantity decimal.Decimal
	UnitPrice        decimal.Decimal
	// Config carries model-specific extras. For ModelCodecMultiplier:
	// {"codec_multipliers": {"h264": 1.0, "hevc": 1.5, ...}}
	Config map[string]any
}

// Input is the rating engine's read-only input.
type Input struct {
	Currency     string
	BasePrice    decimal.Decimal
	Rules        []Rule
	Usage        map[Meter]decimal.Decimal
	CodecSeconds map[string]decimal.Decimal // per-codec seconds for processing_seconds
	PeriodStart  time.Time
	PeriodEnd    time.Time
}

// LineItem is one charge row. LineKey is the stable identity used for
// idempotent upserts on (invoice_id, line_key).
type LineItem struct {
	LineKey          string
	Meter            Meter // empty for base_subscription
	Description      string
	Quantity         decimal.Decimal
	IncludedQuantity decimal.Decimal
	BillableQuantity decimal.Decimal
	UnitPrice        decimal.Decimal
	Amount           decimal.Decimal
	Currency         string
}

// LineKeyBaseSubscription is the well-known key for the base subscription line.
const LineKeyBaseSubscription = "base_subscription"

// Result is the output of Rate.
type Result struct {
	BaseLine    LineItem
	UsageLines  []LineItem
	BaseAmount  decimal.Decimal
	UsageAmount decimal.Decimal
	TotalAmount decimal.Decimal
}
