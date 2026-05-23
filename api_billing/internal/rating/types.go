// Package rating is a pure billing engine: it turns metered usage and
// pricing rules into invoice line items. It has no DB, gRPC, or Stripe
// dependency — handlers load effective rules and usage, call Rate, and
// persist or return the result.
package rating

import (
	"time"

	"github.com/shopspring/decimal"
)

// Meter identifies a canonical metered quantity. The rating engine treats the
// name as data: new marketplace or advanced-processing meters do not require a
// code change as long as producers write the same usage_type and pricing rules
// reference that meter.
type Meter string

const (
	MeterDeliveredMinutes    Meter = "delivered_minutes"
	MeterIngressGB           Meter = "ingress_gb"
	MeterEgressGB            Meter = "egress_gb"
	MeterStorageGBSecondsHot Meter = "storage_gb_seconds_hot"
	MeterStorageGBSecondsCld Meter = "storage_gb_seconds_cold"
	MeterMediaSeconds        Meter = "media_seconds"
)

// ValidMeter reports whether m is a valid canonical meter key. Keep this
// syntactic instead of an enum so cluster_pricing.metered_rates and future
// meter producers can add product-shaped meters without widening a CHECK
// constraint or rebuilding the rating package.
func ValidMeter(m Meter) bool {
	s := string(m)
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 0 && r >= 'a' && r <= 'z':
			continue
		case i > 0 && r >= 'a' && r <= 'z':
			continue
		case i > 0 && r >= '0' && r <= '9':
			continue
		case i > 0 && r == '_':
			continue
		default:
			return false
		}
	}
	return true
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
	// Config carries model-specific extras. For ModelCodecMultiplier,
	// codec_multipliers keys can be plain codecs ("h264") or joint
	// process/codec keys ("Livepeer:h264", "AV:h264").
	Config map[string]any
}

// Input is the rating engine's read-only input.
type Input struct {
	Currency  string
	BasePrice decimal.Decimal
	Rules     []Rule
	Usage     map[Meter]decimal.Decimal
	// Breakdowns carries model-specific dimensional quantities keyed by meter.
	// For ModelCodecMultiplier, the inner map is breakdown key -> seconds;
	// keys can be plain codecs or joint process/codec values.
	Breakdowns map[Meter]map[string]decimal.Decimal
	// CodecSeconds is a convenience shortcut for the per-codec breakdown of
	// the media_seconds rule. Equivalent to Breakdowns[MeterMediaSeconds];
	// callers may populate either, but Breakdowns wins if both are set.
	CodecSeconds map[string]decimal.Decimal
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
