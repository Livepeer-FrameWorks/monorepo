package tools

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// stream_health_5m averages the primary track's Mist kbits, so the tool must
// report that value as kbps, not divide it again (a 5.8 Mbps stream read as 5).
func TestGetStreamHealthReportsKbpsUnchanged(t *testing.T) {
	fake := &clientstest.FakePeriscope{
		GetStreamHealth5mFn: func(_ context.Context, _ string, _ string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealth5MResponse, error) {
			return &periscopepb.GetStreamHealth5MResponse{Records: []*periscopepb.StreamHealth5M{
				{AvgBitrate: 5800, AvgFps: 30, QualityTier: "1080p30 H264"},
				{AvgBitrate: 5600, AvgFps: 30, QualityTier: "1080p30 H264"},
			}}, nil
		},
		GetStreamHealthMetricsFn: func(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			return &periscopepb.GetStreamHealthMetricsResponse{}, nil
		},
	}
	res, out, err := handleGetStreamHealth(clientstest.AuthedCtx("t1"), GetStreamHealthInput{StreamID: "11111111-1111-4111-8111-000000000001"},
		clientstest.Clients(clientstest.WithPeriscope(fake)), clientstest.DiscardLogger())
	if err != nil || res.IsError {
		t.Fatalf("stream health failed: err=%v text=%s", err, extractToolText(res))
	}
	result := out.(DiagnosticResult)
	if got := result.Metrics["avg_bitrate_kbps"]; got != int64(5700) {
		t.Fatalf("avg_bitrate_kbps = %v, want 5700", got)
	}
	if !strings.Contains(result.Analysis, "5700 kbps") {
		t.Fatalf("analysis reports the wrong bitrate: %q", result.Analysis)
	}
}
