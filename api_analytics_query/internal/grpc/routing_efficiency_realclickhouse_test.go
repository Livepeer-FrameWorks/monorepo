//go:build schema_verify

package grpc

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestRoutingEfficiencyWithoutMeasurements_RealClickHouse covers routing
// decisions that carry no distance or latency (both columns are Nullable) and
// a window with no decisions at all. avg() over only NULLs or over no rows is
// NULL in ClickHouse; the summary reports those averages as 0 next to the
// decision count instead of failing the RPC.
func TestRoutingEfficiencyWithoutMeasurements_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLoggerWithService("routing-efficiency-real-ch-test"))
	ctx := serviceTestContext()
	const tenantID = "5eed517e-ba5e-da7a-517e-ba5eda7a00e1"
	const streamID = "5eedfeed-11fe-ca57-feed-11feca5700e1"
	at := time.Now().UTC().Truncate(time.Minute).Add(-30 * time.Minute)

	insert := `INSERT INTO periscope.routing_decisions
		(timestamp, tenant_id, stream_id, internal_name, selected_node, status, details, score, client_ip, client_country,
		 client_latitude, client_longitude, node_latitude, node_longitude, node_name, routing_distance_km, latency_ms)
		VALUES (?, ?, ?, 'routing-stream', 'edge-1', ?, '', 0, '', ?, 0, 0, 0, 0, 'edge-1', ?, ?)`
	decide := func(offset time.Duration, decision, country string, distance *float64, latency *float32) {
		t.Helper()
		if _, err := db.ExecContext(ctx, insert, at.Add(offset), tenantID, streamID, decision, country, distance, latency); err != nil {
			t.Fatal(err)
		}
	}
	window := func(start, end time.Time) *commonpb.TimeRange {
		return &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)}
	}

	decide(time.Second, "success", "NL", nil, nil)
	decide(2*time.Second, "failed", "DE", nil, nil)
	unmeasured, err := server.GetRoutingEfficiency(ctx, &periscopepb.GetRoutingEfficiencyRequest{
		TenantId:  tenantID,
		TimeRange: window(at.Add(-time.Minute), at.Add(time.Minute)),
	})
	if err != nil {
		t.Fatalf("GetRoutingEfficiency over unmeasured decisions: %v", err)
	}
	if got := unmeasured.GetSummary(); got.GetTotalDecisions() != 2 || got.GetSuccessCount() != 1 ||
		got.GetAvgRoutingDistance() != 0 || got.GetAvgLatencyMs() != 0 || len(got.GetTopCountries()) != 2 {
		t.Fatalf("unmeasured summary = %+v", got)
	}

	distance, latency := 120.0, float32(40)
	decide(3*time.Second, "success", "NL", &distance, &latency)
	mixed, err := server.GetRoutingEfficiency(ctx, &periscopepb.GetRoutingEfficiencyRequest{
		TenantId:  tenantID,
		TimeRange: window(at.Add(-time.Minute), at.Add(time.Minute)),
	})
	if err != nil {
		t.Fatalf("GetRoutingEfficiency over mixed decisions: %v", err)
	}
	if got := mixed.GetSummary(); got.GetTotalDecisions() != 3 || got.GetAvgRoutingDistance() != 120 || got.GetAvgLatencyMs() != 40 {
		t.Fatalf("mixed summary = %+v, want averages over measured decisions only", got)
	}

	empty, err := server.GetRoutingEfficiency(ctx, &periscopepb.GetRoutingEfficiencyRequest{
		TenantId:  tenantID,
		TimeRange: window(at.Add(-48*time.Hour), at.Add(-47*time.Hour)),
	})
	if err != nil {
		t.Fatalf("GetRoutingEfficiency over an empty window: %v", err)
	}
	if got := empty.GetSummary(); got.GetTotalDecisions() != 0 || got.GetSuccessRate() != 0 ||
		got.GetAvgRoutingDistance() != 0 || got.GetAvgLatencyMs() != 0 || len(got.GetTopCountries()) != 0 {
		t.Fatalf("empty-window summary = %+v", got)
	}
}
