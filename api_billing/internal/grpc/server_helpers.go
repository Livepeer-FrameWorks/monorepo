package grpc

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// buildRatingInputForUsage constructs a rating.Input for GetTenantUsage.
// BasePrice is the tier's monthly fee so the response surfaces it via
// BaseAmount (informational); the preview's metered total_cost still excludes
// it — TotalCost == UsageAmount per the proto contract.
//
// usage carries canonical usage_type → total values. The map is not limited to
// today's billed meters; rating rules decide which meters produce lines.
// codecBreakdowns carries per-meter codec breakdowns extracted from
// usage_details.codec_seconds JSON.
func buildRatingInputForUsage(usage map[string]float64, codecBreakdowns map[string]map[string]float64, currency string, basePrice decimal.Decimal, rules []rating.Rule) rating.Input {
	usageMap := make(map[rating.Meter]decimal.Decimal, len(usage))
	for meter, total := range usage {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			continue
		}
		usageMap[m] = decimal.NewFromFloat(total)
	}
	breakdowns := map[rating.Meter]map[string]decimal.Decimal{}
	for meter, codecTotals := range codecBreakdowns {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			continue
		}
		codecs := map[string]decimal.Decimal{}
		for codec, total := range codecTotals {
			if total != 0 {
				codecs[codec] = decimal.NewFromFloat(total)
			}
		}
		if len(codecs) > 0 {
			breakdowns[m] = codecs
		}
	}
	return rating.Input{
		Currency:          currency,
		BasePrice:         basePrice,
		Rules:             rules,
		Usage:             usageMap,
		Breakdowns:        breakdowns,
		CodecSeconds:      breakdowns[rating.MeterMediaSeconds],
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
func lineItemToProto(li rating.LineItem) *pb.LineItem {
	return &pb.LineItem{
		LineKey:          li.LineKey,
		Meter:            string(li.Meter),
		Description:      li.Description,
		Quantity:         li.Quantity.String(),
		IncludedQuantity: li.IncludedQuantity.String(),
		BillableQuantity: li.BillableQuantity.String(),
		UnitPrice:        li.UnitPrice.String(),
		Total:            li.Amount.String(),
		Currency:         li.Currency,
	}
}
