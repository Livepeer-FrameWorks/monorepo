package rating

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

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
	for meter := range in.Usage {
		if !ValidMeter(meter) {
			return Result{}, fmt.Errorf("rating: unsupported usage meter %q", meter)
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
		Unit:             "subscription",
	}

	usageLines := make([]LineItem, 0, len(in.Rules))
	for _, rule := range in.Rules {
		if err := ValidateRule(rule); err != nil {
			return Result{}, fmt.Errorf("rating: %w", err)
		}
		if rule.Currency != currency {
			return Result{}, fmt.Errorf("%w: rule for meter %q has currency %q, input has %q",
				ErrCurrencyMismatch, rule.Meter, rule.Currency, currency)
		}
		switch rule.Model {
		case ModelTieredGraduated:
			line, ok := rateTieredGraduated(rule, in.Usage[rule.Meter], ratedUnit(rule, quantityUnit(in.Quantities, rule.Meter)), currency)
			if ok {
				usageLines = append(usageLines, line)
			}
		case ModelAllUsage:
			line, ok := rateAllUsage(rule, in.Usage[rule.Meter], ratedUnit(rule, quantityUnit(in.Quantities, rule.Meter)), currency)
			if ok {
				usageLines = append(usageLines, line)
			}
		case ModelDimensioned:
			lines, dimensionErr := rateDimensioned(rule, quantitiesForMeter(in.Quantities, rule.Meter), currency)
			if dimensionErr != nil {
				return Result{}, dimensionErr
			}
			usageLines = append(usageLines, lines...)
		}
	}

	pricedMeters := make(map[Meter]struct{}, len(in.Rules))
	for _, rule := range in.Rules {
		pricedMeters[rule.Meter] = struct{}{}
	}
	for _, quantity := range in.Quantities {
		if quantity.Quantity.IsZero() {
			continue
		}
		if _, priced := pricedMeters[quantity.Meter]; priced {
			continue
		}
		usageLines = append(usageLines, dimensionLine(quantity, decimal.Zero, decimal.Zero, currency))
	}

	// Sort usage lines by LineKey for determinism.
	sort.Slice(usageLines, func(i, j int) bool {
		return usageLines[i].LineKey < usageLines[j].LineKey
	})

	// Stamp each line's pre-waiver amount and total the unwaived metered amount.
	// Index loop: ranging by value copies the struct and the writes no-op.
	grossUsageAmount := decimal.Zero
	for i := range usageLines {
		usageLines[i].GrossAmount = usageLines[i].Amount
		grossUsageAmount = grossUsageAmount.Add(usageLines[i].GrossAmount)
	}

	// Beta waiver: zero every usage line (incl. negative correction lines) so
	// metered usage cannot charge no matter how wrong the quantity is. Quantities
	// and unit prices stay real for display; GrossAmount preserves the pre-waiver
	// value. The base line is untouched.
	if in.WaiveUsageCharges {
		for i := range usageLines {
			usageLines[i].Amount = decimal.Zero
		}
	}

	usageAmount := decimal.Zero
	for _, l := range usageLines {
		usageAmount = usageAmount.Add(l.Amount)
	}

	return Result{
		BaseLine:         base,
		UsageLines:       usageLines,
		BaseAmount:       base.Amount,
		UsageAmount:      usageAmount,
		GrossUsageAmount: grossUsageAmount,
		TotalAmount:      base.Amount.Add(usageAmount),
	}, nil
}

// toRatedUnits converts a rule's stored unit to its rated unit. Storage meters
// are stored as GiB-seconds internally but priced per GiB-hour, so the engine
// divides by 3600 before multiplying by unit_price. Custom meters can set
// config.rated_quantity_divisor for the same behavior without code changes.
func toRatedUnits(rule Rule, quantity decimal.Decimal) decimal.Decimal {
	if divisor, ok := decimalFromAny(rule.Config["rated_quantity_divisor"]); ok && divisor.IsPositive() {
		return quantity.Div(divisor)
	}
	switch rule.Meter {
	case MeterStorageGBSecondsHot, MeterStorageGBSecondsCld:
		return quantity.Div(decimal.NewFromInt(3600))
	default:
		return quantity
	}
}

func quantityUnit(quantities []DimensionedQuantity, meter Meter) string {
	for _, quantity := range quantities {
		if quantity.Meter == meter && quantity.Unit != "" {
			return quantity.Unit
		}
	}
	switch meter {
	case MeterDeliveredMinutes:
		return "minute"
	case MeterIngressGB, MeterEgressGB:
		return "gibibyte"
	case MeterStorageGBSecondsHot, MeterStorageGBSecondsCld:
		return "gibibyte_second"
	case MeterMediaSeconds:
		return "second"
	default:
		return "unit"
	}
}

func ratedUnit(rule Rule, sourceUnit string) string {
	if configured, ok := rule.Config["rated_unit"].(string); ok && configured != "" {
		return configured
	}
	switch rule.Meter {
	case MeterStorageGBSecondsHot, MeterStorageGBSecondsCld:
		return "gibibyte_hour"
	default:
		return sourceUnit
	}
}

// rateTieredGraduated charges (quantity - included) * unit_price after
// unit conversion for the meter (storage → GiB-hours, others pass through).
// Returns ok=false when the line would be a $0 row with no meaningful info.
func rateTieredGraduated(rule Rule, quantity decimal.Decimal, unit, currency string) (LineItem, bool) {
	if quantity.IsZero() {
		return LineItem{}, false
	}
	rated := toRatedUnits(rule, quantity)
	if rated.IsNegative() {
		amount := rated.Mul(rule.UnitPrice)
		return LineItem{
			LineKey:          "meter:" + string(rule.Meter),
			Meter:            rule.Meter,
			Description:      describeMeter(rule),
			Quantity:         rated,
			IncludedQuantity: decimal.Zero,
			BillableQuantity: rated,
			UnitPrice:        rule.UnitPrice,
			Amount:           amount,
			Currency:         currency,
			Unit:             unit,
		}, true
	}
	billable := rated.Sub(rule.IncludedQuantity)
	if billable.IsNegative() {
		billable = decimal.Zero
	}
	amount := billable.Mul(rule.UnitPrice)
	// Emit the line even when amount is zero but quantity > 0, so previews
	// surface "you used X of Y included." Skip only when usage is exactly zero.
	return LineItem{
		LineKey:          "meter:" + string(rule.Meter),
		Meter:            rule.Meter,
		Description:      describeMeter(rule),
		Quantity:         rated,
		IncludedQuantity: rule.IncludedQuantity,
		BillableQuantity: billable,
		UnitPrice:        rule.UnitPrice,
		Amount:           amount,
		Currency:         currency,
		Unit:             unit,
	}, true
}

// rateAllUsage charges the full quantity at unit_price, converted to the
// meter's rated unit first.
func rateAllUsage(rule Rule, quantity decimal.Decimal, unit, currency string) (LineItem, bool) {
	if quantity.IsZero() {
		return LineItem{}, false
	}
	rated := toRatedUnits(rule, quantity)
	amount := rated.Mul(rule.UnitPrice)
	return LineItem{
		LineKey:          "meter:" + string(rule.Meter),
		Meter:            rule.Meter,
		Description:      describeMeter(rule),
		Quantity:         rated,
		IncludedQuantity: decimal.Zero,
		BillableQuantity: rated,
		UnitPrice:        rule.UnitPrice,
		Amount:           amount,
		Currency:         currency,
		Unit:             unit,
	}, true
}

func quantitiesForMeter(all []DimensionedQuantity, meter Meter) []DimensionedQuantity {
	out := make([]DimensionedQuantity, 0)
	for _, quantity := range all {
		if quantity.Meter == meter {
			out = append(out, quantity)
		}
	}
	sort.Slice(out, func(i, j int) bool { return dimensionKey(out[i].Dimensions) < dimensionKey(out[j].Dimensions) })
	return out
}

func rateDimensioned(rule Rule, quantities []DimensionedQuantity, currency string) ([]LineItem, error) {
	includedRemaining := rule.IncludedQuantity
	out := make([]LineItem, 0, len(quantities))
	for _, quantity := range quantities {
		if quantity.Quantity.IsZero() {
			continue
		}
		unitPrice, err := dimensionUnitPrice(rule, quantity.Dimensions)
		if err != nil {
			return nil, err
		}
		rated := toRatedUnits(rule, quantity.Quantity)
		quantity.Quantity = rated
		quantity.Unit = ratedUnit(rule, quantity.Unit)
		included := decimal.Zero
		if rated.IsPositive() && includedRemaining.IsPositive() {
			included = decimal.Min(rated, includedRemaining)
			includedRemaining = includedRemaining.Sub(included)
		}
		out = append(out, dimensionLine(quantity, included, unitPrice, currency))
	}
	return out, nil
}

func dimensionUnitPrice(rule Rule, dimensions map[string]string) (decimal.Decimal, error) {
	selected := rule.UnitPrice
	selectedSpecificity := -1
	rawRates, ok := rule.Config["rates"]
	if !ok {
		return selected, nil
	}
	rates, ok := rawRates.([]any)
	if !ok {
		return decimal.Zero, fmt.Errorf("rating: rule for meter %q has invalid rates", rule.Meter)
	}
	for _, rawRate := range rates {
		rateMap, ok := rawRate.(map[string]any)
		if !ok {
			return decimal.Zero, fmt.Errorf("rating: rule for meter %q contains an invalid rate", rule.Meter)
		}
		selectors := map[string]string{}
		if rawSelectors, exists := rateMap["selectors"]; exists {
			switch values := rawSelectors.(type) {
			case map[string]any:
				for key, value := range values {
					text, isString := value.(string)
					if !isString {
						return decimal.Zero, fmt.Errorf("rating: selector %q for meter %q is not a string", key, rule.Meter)
					}
					selectors[key] = text
				}
			case map[string]string:
				selectors = values
			default:
				return decimal.Zero, fmt.Errorf("rating: selectors for meter %q are invalid", rule.Meter)
			}
		}
		matches := true
		for key, value := range selectors {
			if dimensions[key] != value {
				matches = false
				break
			}
		}
		if !matches || len(selectors) <= selectedSpecificity {
			continue
		}
		price, ok := decimalFromAny(rateMap["unit_price"])
		if !ok || price.IsNegative() {
			return decimal.Zero, fmt.Errorf("rating: unit_price for meter %q selector is invalid", rule.Meter)
		}
		selected = price
		selectedSpecificity = len(selectors)
	}
	return selected, nil
}

func dimensionLine(quantity DimensionedQuantity, included, unitPrice decimal.Decimal, currency string) LineItem {
	billable := quantity.Quantity.Sub(included)
	if quantity.Quantity.IsPositive() && billable.IsNegative() {
		billable = decimal.Zero
	}
	return LineItem{
		LineKey:          "meter:" + string(quantity.Meter) + ":dimensions:" + dimensionKey(quantity.Dimensions),
		Meter:            quantity.Meter,
		Description:      dimensionDescription(quantity.Meter, quantity.Dimensions),
		Quantity:         quantity.Quantity,
		IncludedQuantity: included,
		BillableQuantity: billable,
		UnitPrice:        unitPrice,
		Amount:           billable.Mul(unitPrice),
		Currency:         currency,
		Unit:             quantity.Unit,
		Dimensions:       quantity.Dimensions,
	}
}

func dimensionKey(dimensions map[string]string) string {
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var material strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&material, "%d:%s=%d:%s;", len(key), key, len(dimensions[key]), dimensions[key])
	}
	sum := sha256.Sum256([]byte(material.String()))
	return hex.EncodeToString(sum[:])[:16]
}

func dimensionDescription(meter Meter, dimensions map[string]string) string {
	base := humanizeMeter(meter)
	if len(dimensions) == 0 {
		return base
	}
	keys := make([]string, 0, len(dimensions))
	for key := range dimensions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+dimensions[key])
	}
	return base + " (" + strings.Join(parts, ", ") + ")"
}

func describeMeter(rule Rule) string {
	if desc, ok := rule.Config["description"].(string); ok && strings.TrimSpace(desc) != "" {
		return strings.TrimSpace(desc)
	}
	switch rule.Meter {
	case MeterDeliveredMinutes:
		return "Delivered minutes"
	case MeterEgressGB:
		return "Bandwidth"
	case MeterStorageGBSecondsHot:
		return "Hot storage"
	case MeterStorageGBSecondsCld:
		return "Cold storage"
	case MeterMediaSeconds:
		return "Media processing"
	default:
		return humanizeMeter(rule.Meter)
	}
}

func humanizeMeter(m Meter) string {
	parts := strings.Split(string(m), "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
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
