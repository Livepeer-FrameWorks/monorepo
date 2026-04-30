package grpc

import (
	"github.com/shopspring/decimal"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/rating"
	pb "frameworks/pkg/proto"
)

// buildRatingInputForUsage constructs a rating.Input for GetTenantUsage.
// BasePrice is the tier's monthly fee so the response surfaces it via
// BaseAmount (informational); the preview's metered total_cost still excludes
// it — TotalCost == UsageAmount per the proto contract.
func buildRatingInputForUsage(usage map[string]float64, tier *billingpkg.EffectiveTier) rating.Input {
	viewerHours := decimal.NewFromFloat(usage["viewer_hours"])
	usageMap := map[rating.Meter]decimal.Decimal{
		rating.MeterDeliveredMinutes: viewerHours.Mul(decimal.NewFromInt(60)),
		rating.MeterAverageStorageGB: decimal.NewFromFloat(usage["average_storage_gb"]),
		rating.MeterAIGPUHours:       decimal.NewFromFloat(usage["gpu_hours"]),
	}
	codecs := map[string]decimal.Decimal{}
	for _, c := range []string{"h264", "hevc", "vp9", "av1", "aac", "opus"} {
		total := usage["livepeer_"+c+"_seconds"] + usage["native_av_"+c+"_seconds"]
		if total > 0 {
			codecs[c] = decimal.NewFromFloat(total)
		}
	}
	return rating.Input{
		Currency:     tier.Currency,
		BasePrice:    tier.BasePrice,
		Rules:        tier.Rules,
		Usage:        usageMap,
		CodecSeconds: codecs,
	}
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
