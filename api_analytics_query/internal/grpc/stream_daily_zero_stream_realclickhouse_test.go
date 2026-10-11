//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Rows rolled up before artifact views were kept out of the daily stream
// rollup hold uploaded-VOD and clip views under the zero stream id. Those are
// not a stream, so the daily stream listing and its count leave them out.
func TestStreamDailyAnalyticsOmitsTheZeroStream_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	ctx := context.Background()
	const tenantID = "7eed517e-ba5e-4a7a-817e-ba5eda7a0004"
	const streamID = "7eedfeed-11fe-4a57-8eed-11feca570004"
	day := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO periscope.stream_analytics_daily_store
		(day, tenant_id, stream_id, internal_name, total_views, unique_viewers_state, egress_bytes, refresh_version_ms)
		SELECT toDate(?), toUUID(?), toUUID(?), 'live+daily', 2, uniqCombinedState('viewer-a'), 1024, now64(3)
		UNION ALL
		SELECT toDate(?), toUUID(?), toUUID('00000000-0000-0000-0000-000000000000'), 'vod+20261005175800a1b2c3d4e5f60718', 5, uniqCombinedState('viewer-b'), 2048, now64(3)
	`, day, tenantID, streamID, day, tenantID); err != nil {
		t.Fatal(err)
	}

	server := NewPeriscopeServer(db, logging.NewLogger())
	serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
	resp, err := server.GetStreamAnalyticsDaily(serviceCtx, &periscopepb.GetStreamAnalyticsDailyRequest{
		TenantId:  tenantID,
		TimeRange: &commonpb.TimeRange{Start: timestamppb.New(day.Add(-time.Hour)), End: timestamppb.New(day.Add(25 * time.Hour))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetRecords(); len(got) != 1 || got[0].GetStreamId() != streamID || got[0].GetTotalViews() != 2 {
		t.Fatalf("daily stream rows = %+v, want only stream %s with 2 views", got, streamID)
	}
	if total := resp.GetPagination().GetTotalCount(); total != 1 {
		t.Fatalf("daily stream row count = %d, want 1", total)
	}
}
