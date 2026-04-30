package rating

import (
	"errors"
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

var (
	// ErrCurrencyMismatch is returned when a Rule's currency does not match Input.Currency.
	ErrCurrencyMismatch = errors.New("rating: rule currency does not match input currency")
	// ErrUnknownModel is returned when a Rule's Model is not recognized.
	ErrUnknownModel = errors.New("rating: unknown pricing model")
)

// Rate turns Input into a Result. It is pure: same input → same output.
// Money math uses decimal.Decimal; no float rounding.
func Rate(in Input) (Result, error) {
	currency := in.Currency
	if currency == "" {
		return Result{}, errors.New("rating: input currency is empty")
	}
	if in.BasePrice.IsNegative() {
		return Result{}, errors.New("rating: base price cannot be negative")
	}
	for meter, quantity := range in.Usage {
		if !ValidMeter(meter) {
			return Result{}, fmt.Errorf("rating: unsupported usage meter %q", meter)
		}
		if quantity.IsNegative() {
			return Result{}, fmt.Errorf("rating: usage for meter %q cannot be negative", meter)
		}
	}
	for codec, seconds := range in.CodecSeconds {
		if seconds.IsNegative() {
			return Result{}, fmt.Errorf("rating: codec seconds for %q cannot be negative", codec)
		}
	}

	base := LineItem{
		LineKey:          LineKeyBaseSubscription,
		Description:      "Base subscription",
		Quantity:         decimal.NewFromInt(1),
		IncludedQuantity: decimal.Zero,
		BillableQuantity: decimal.NewFromInt(1),
		UnitPrice:        in.BasePrice,
		Amount:           in.BasePrice,
		Currency:         currency,
	}

	usageLines := make([]LineItem, 0, len(in.Rules))
	for _, rule := range in.Rules {
		if !ValidMeter(rule.Meter) {
			return Result{}, fmt.Errorf("rating: unsupported meter %q", rule.Meter)
		}
		if !ValidModel(rule.Model) {
			return Result{}, fmt.Errorf("%w: %q (meter %q)", ErrUnknownModel, rule.Model, rule.Meter)
		}
		if rule.Currency == "" {
			return Result{}, fmt.Errorf("rating: rule for meter %q has empty currency", rule.Meter)
		}
		if rule.Currency != currency {
			return Result{}, fmt.Errorf("%w: rule for meter %q has currency %q, input has %q",
				ErrCurrencyMismatch, rule.Meter, rule.Currency, currency)
		}
		if rule.IncludedQuantity.IsNegative() {
			return Result{}, fmt.Errorf("rating: rule for meter %q has negative included quantity", rule.Meter)
		}
		if rule.UnitPrice.IsNegative() {
			return Result{}, fmt.Errorf("rating: rule for meter %q has negative unit price", rule.Meter)
		}
		switch rule.Model {
		case ModelTieredGraduated:
			line, ok := rateTieredGraduated(rule, in.Usage[rule.Meter], currency)
			if ok {
				usageLines = append(usageLines, line)
			}
		case ModelAllUsage:
			line, ok := rateAllUsage(rule, in.Usage[rule.Meter], currency)
			if ok {
				usageLines = append(usageLines, line)
			}
		case ModelCodecMultiplier:
			lines := rateCodecMultiplier(rule, in.CodecSeconds, currency)
			usageLines = append(usageLines, lines...)
		}
	}

	// Sort usage lines by LineKey for determinism.
	sort.Slice(usageLines, func(i, j int) bool {
		return usageLines[i].LineKey < usageLines[j].LineKey
	})

	usageAmount := decimal.Zero
	for _, l := range usageLines {
		usageAmount = usageAmount.Add(l.Amount)
	}

	return Result{
		BaseLine:    base,
		UsageLines:  usageLines,
		BaseAmount:  base.Amount,
		UsageAmount: usageAmount,
		TotalAmount: base.Amount.Add(usageAmount),
	}, nil
}

// rateTieredGraduated charges (quantity - included) * unit_price.
// Returns ok=false when the line would be a $0 row with no meaningful info.
func rateTieredGraduated(rule Rule, quantity decimal.Decimal, currency string) (LineItem, bool) {
	if quantity.IsZero() {
		return LineItem{}, false
	}
	billable := quantity.Sub(rule.IncludedQuantity)
	if billable.IsNegative() {
		billable = decimal.Zero
	}
	amount := billable.Mul(rule.UnitPrice)
	// Emit the line even when amount is zero but quantity > 0, so previews
	// surface "you used X of Y included." Skip only when usage is exactly zero.
	return LineItem{
		LineKey:          "meter:" + string(rule.Meter),
		Meter:            rule.Meter,
		Description:      describeMeter(rule.Meter),
		Quantity:         quantity,
		IncludedQuantity: rule.IncludedQuantity,
		BillableQuantity: billable,
		UnitPrice:        rule.UnitPrice,
		Amount:           amount,
		Currency:         currency,
	}, true
}

// rateAllUsage charges the full quantity at unit_price.
func rateAllUsage(rule Rule, quantity decimal.Decimal, currency string) (LineItem, bool) {
	if quantity.IsZero() {
		return LineItem{}, false
	}
	amount := quantity.Mul(rule.UnitPrice)
	return LineItem{
		LineKey:          "meter:" + string(rule.Meter),
		Meter:            rule.Meter,
		Description:      describeMeter(rule.Meter),
		Quantity:         quantity,
		IncludedQuantity: decimal.Zero,
		BillableQuantity: quantity,
		UnitPrice:        rule.UnitPrice,
		Amount:           amount,
		Currency:         currency,
	}, true
}

// rateCodecMultiplier emits one line per codec with non-zero seconds.
//
// Quantity is normalized to minutes and unit_price to the effective per-minute
// rate (rule.UnitPrice * codec_multiplier) so the line item satisfies the audit
// invariant billable_quantity * unit_price = amount.
func rateCodecMultiplier(rule Rule, codecSeconds map[string]decimal.Decimal, currency string) []LineItem {
	multipliers, ok := rule.Config["codec_multipliers"].(map[string]any)
	if !ok || len(multipliers) == 0 || rule.UnitPrice.IsZero() {
		return nil
	}

	codecs := make([]string, 0, len(multipliers))
	for codec := range multipliers {
		codecs = append(codecs, codec)
	}
	sort.Strings(codecs)

	sixty := decimal.NewFromInt(60)
	out := make([]LineItem, 0, len(codecs))
	for _, codec := range codecs {
		seconds := codecSeconds[codec]
		if seconds.IsZero() {
			continue
		}
		mult, ok := decimalFromAny(multipliers[codec])
		if !ok || mult.IsZero() {
			continue
		}
		minutes := seconds.Div(sixty)
		effectiveUnitPrice := rule.UnitPrice.Mul(mult)
		amount := minutes.Mul(effectiveUnitPrice)
		out = append(out, LineItem{
			LineKey:          "meter:" + string(rule.Meter) + ":codec:" + codec,
			Meter:            rule.Meter,
			Description:      "Processing (" + codec + ")",
			Quantity:         minutes,
			IncludedQuantity: decimal.Zero,
			BillableQuantity: minutes,
			UnitPrice:        effectiveUnitPrice,
			Amount:           amount,
			Currency:         currency,
		})
	}
	return out
}

func describeMeter(m Meter) string {
	switch m {
	case MeterDeliveredMinutes:
		return "Delivered minutes"
	case MeterAverageStorageGB:
		return "Recording storage (avg GB)"
	case MeterAIGPUHours:
		return "AI GPU hours"
	case MeterProcessingSeconds:
		return "Transcoding"
	default:
		return string(m)
	}
}

func decimalFromAny(v any) (decimal.Decimal, bool) {
	switch x := v.(type) {
	case decimal.Decimal:
		return x, true
	case float64:
		return decimal.NewFromFloat(x), true
	case float32:
		return decimal.NewFromFloat(float64(x)), true
	case int:
		return decimal.NewFromInt(int64(x)), true
	case int64:
		return decimal.NewFromInt(x), true
	case string:
		d, err := decimal.NewFromString(x)
		if err != nil {
			return decimal.Zero, false
		}
		return d, true
	default:
		return decimal.Zero, false
	}
}
