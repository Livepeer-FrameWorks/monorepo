// Package rating is a pure billing engine: it turns metered usage and
// pricing rules into invoice line items. It has no DB, gRPC, or Stripe
// dependency — handlers load effective rules and usage, call Rate, and
// persist or return the result.
package rating

import (
	"fmt"
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
	// ModelDimensioned applies the most-specific selector rate to each bounded
	// dimension bucket while sharing one included allowance across the meter.
	ModelDimensioned Model = "dimensioned"
)

// ValidModel reports whether m is one of the pricing models Rate can execute.
func ValidModel(m Model) bool {
	switch m {
	case ModelTieredGraduated, ModelAllUsage, ModelDimensioned:
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
	// Config carries model-specific extras. ModelDimensioned accepts rates:
	// [{"selectors":{"output_codec":"av1"},"unit_price":"0.01"}].
	Config map[string]any
}

// ValidateRuleShape checks the durable pricing-rule fields that must be true
// before a rule is written or rated. It deliberately does not require Currency
// because cluster metered-rate JSON inherits currency from the cluster row.
func ValidateRuleShape(rule Rule) error {
	if !ValidMeter(rule.Meter) {
		return fmt.Errorf("invalid meter %q", rule.Meter)
	}
	if !ValidModel(rule.Model) {
		return fmt.Errorf("%w: %q (meter %q)", ErrUnknownModel, rule.Model, rule.Meter)
	}
	if rule.IncludedQuantity.IsNegative() {
		return fmt.Errorf("rule for meter %q has negative included quantity", rule.Meter)
	}
	if rule.UnitPrice.IsNegative() {
		return fmt.Errorf("rule for meter %q has negative unit price", rule.Meter)
	}
	if rule.Model == ModelDimensioned {
		if rates, ok := rule.Config["rates"]; ok {
			if _, ok := rates.([]any); !ok {
				return fmt.Errorf("rule for meter %q config.rates must be an array", rule.Meter)
			}
		}
	}
	return nil
}

// ValidateRule checks a complete rating rule, including its currency.
func ValidateRule(rule Rule) error {
	if err := ValidateRuleShape(rule); err != nil {
		return err
	}
	if rule.Currency == "" {
		return fmt.Errorf("rule for meter %q has empty currency", rule.Meter)
	}
	return nil
}

// Input is the rating engine's read-only input.
type Input struct {
	Currency    string
	BasePrice   decimal.Decimal
	Rules       []Rule
	Usage       map[Meter]decimal.Decimal
	Quantities  []DimensionedQuantity
	PeriodStart time.Time
	PeriodEnd   time.Time
	// WaiveUsageCharges zeroes every usage line's Amount (keeping quantities and
	// unit prices) so metered usage rates to 0 while the base subscription still
	// charges. Each line's pre-waiver amount is preserved in LineItem.GrossAmount.
	// Beta safety lever; the base line is never affected.
	WaiveUsageCharges bool
}

type DimensionedQuantity struct {
	Meter      Meter
	Unit       string
	Dimensions map[string]string
	Quantity   decimal.Decimal
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
	// GrossAmount is the pre-waiver amount (== Amount when usage is not waived).
	// Internal to rating: drives the genuinely-waived stamping decision and the
	// invoice-level gross_metered_amount total. Not persisted per-line.
	GrossAmount decimal.Decimal
	Currency    string
	Unit        string
	Dimensions  map[string]string
}

// LineKeyBaseSubscription is the well-known key for the base subscription line.
const LineKeyBaseSubscription = "base_subscription"

// Result is the output of Rate.
type Result struct {
	BaseLine   LineItem
	UsageLines []LineItem
	BaseAmount decimal.Decimal
	// UsageAmount is the net metered amount (0 when usage is waived).
	UsageAmount decimal.Decimal
	// GrossUsageAmount is the unwaived metered total across all usage lines
	// (each line's pre-waiver amount). Equals UsageAmount when usage is not
	// waived. Display-only; never feeds a charge.
	GrossUsageAmount decimal.Decimal
	TotalAmount      decimal.Decimal
}
