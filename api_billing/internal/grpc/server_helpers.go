package grpc

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/fx"
	"frameworks/api_billing/internal/rating"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// buildRatingInputForUsage constructs a rating.Input for GetTenantUsage.
// BasePrice is the tier's monthly fee so the response surfaces it via
// BaseAmount (informational); the preview's metered total_cost still excludes
// it — TotalCost == UsageAmount per the proto contract.
//
// usage carries canonical usage_type → total values. The map is not limited to
// today's billed meters; rating rules decide which meters produce lines.
func buildRatingInputForUsage(usage map[string]float64, quantities []rating.DimensionedQuantity, currency string, basePrice decimal.Decimal, rules []rating.Rule) rating.Input {
	usageMap := make(map[rating.Meter]decimal.Decimal, len(usage))
	for meter, total := range usage {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			continue
		}
		usageMap[m] = decimal.NewFromFloat(total)
	}
	return rating.Input{
		Currency:          currency,
		BasePrice:         basePrice,
		Rules:             rules,
		Usage:             usageMap,
		Quantities:        quantities,
		WaiveUsageCharges: appconfig.Runtime().WaiveUsageCharges,
	}
}

func clusterScopedLineKey(baseKey, clusterID, periodSuffix string) string {
	const maxLineKeyLen = 128
	candidate := fmt.Sprintf("%s:%s:%s", baseKey, clusterID, periodSuffix)
	if len(candidate) <= maxLineKeyLen {
		return candidate
	}
	sum := sha1.Sum([]byte(clusterID))
	shortID := hex.EncodeToString(sum[:])[:12]
	suffix := fmt.Sprintf(":cluster-%s:%s", shortID, periodSuffix)
	if len(baseKey)+len(suffix) > maxLineKeyLen {
		baseKey = baseKey[:maxLineKeyLen-len(suffix)]
	}
	return baseKey + suffix
}

// lineItemToProto serializes a rating.LineItem into the proto wire shape.
// Decimal fields are encoded as strings to preserve precision.
func lineItemToProto(li rating.LineItem) *purserpb.LineItem {
	dimensions := make(map[string]any, len(li.Dimensions))
	for key, value := range li.Dimensions {
		dimensions[key] = value
	}
	return &purserpb.LineItem{
		LineKey:          li.LineKey,
		Meter:            string(li.Meter),
		Description:      li.Description,
		Quantity:         li.Quantity.String(),
		IncludedQuantity: li.IncludedQuantity.String(),
		BillableQuantity: li.BillableQuantity.String(),
		UnitPrice:        li.UnitPrice.String(),
		Total:            li.Amount.String(),
		Currency:         li.Currency,
		Unit:             li.Unit,
		Dimensions:       mapToProtoStruct(dimensions),
	}
}

// decimalText renders a stored NUMERIC rate without trailing zeros, so the
// identity rate reads "1". Text that does not parse is returned unchanged.
func decimalText(value string) string {
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return value
	}
	return parsed.String()
}

// dateText renders an ECB reference date as YYYY-MM-DD.
func dateText(value time.Time) string {
	return value.UTC().Format(time.DateOnly)
}

// fxConversionProto describes one stored conversion of a money row into the
// EUR ledger.
func fxConversionProto(originalMinor int64, originalCurrency string, eurMinor int64, unitsPerEUR, source string, referenceDate time.Time) *purserpb.FxConversion {
	return &purserpb.FxConversion{
		OriginalAmountCents: originalMinor,
		OriginalCurrency:    strings.TrimSpace(originalCurrency),
		EurAmountCents:      eurMinor,
		UnitsPerEur:         decimalText(unitsPerEUR),
		Source:              source,
		ReferenceDate:       dateText(referenceDate),
	}
}

func fxRecordProto(record fx.Record) *purserpb.FxConversion {
	return fxConversionProto(record.OriginalMinor, record.OriginalCurrency, record.EURMinor, record.UnitsText(), record.Source, record.ReferenceDate)
}

// invoicePresentment copies an invoice's presentment columns onto its proto.
// Drafts and invoices held for review have none and stay unset.
func invoicePresentment(invoice *purserpb.Invoice, amountCents sql.NullInt64, currency, unitsPerEUR string, referenceDate, finalizedAt sql.NullTime) {
	if amountCents.Valid {
		value := amountCents.Int64
		invoice.PresentmentAmountCents = &value
		invoice.PresentmentCurrency = strings.TrimSpace(currency)
		invoice.PresentmentUnitsPerEur = decimalText(unitsPerEUR)
	}
	if referenceDate.Valid {
		invoice.PresentmentReferenceDate = dateText(referenceDate.Time)
	}
	if finalizedAt.Valid {
		invoice.FinalizedAt = timestamppb.New(finalizedAt.Time)
	}
}
