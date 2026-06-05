package resolvers

import (
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"math"
	"testing"
)

func TestSanitizePlatformOverviewGraphQL(t *testing.T) {
	resp := &periscopepb.GetPlatformOverviewResponse{
		AverageViewers:   math.NaN(),
		PeakBandwidth:    math.Inf(1),
		StreamHours:      math.Inf(-1),
		EgressGb:         12.5,
		ViewerHours:      math.NaN(),
		DeliveredMinutes: math.Inf(1),
		IngestHours:      9,
	}

	got := sanitizePlatformOverviewGraphQL(resp)

	if got.AverageViewers != 0 ||
		got.PeakBandwidth != 0 ||
		got.StreamHours != 0 ||
		got.ViewerHours != 0 ||
		got.DeliveredMinutes != 0 {
		t.Fatalf("expected non-finite GraphQL float fields to be zeroed: %+v", got)
	}
	if got.EgressGb != 12.5 || got.IngestHours != 9 {
		t.Fatalf("expected finite GraphQL float fields to be preserved: %+v", got)
	}
}
