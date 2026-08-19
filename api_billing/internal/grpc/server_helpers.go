package grpc

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
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
		WaiveUsageCharges: config.WaiveUsageChargesEnabled(),
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
