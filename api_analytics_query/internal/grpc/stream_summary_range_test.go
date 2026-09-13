package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStreamAnalyticsSummaryIncludesOverlappingBucket(t *testing.T) {
	db, server, mock := newLiveUsageSummaryServer(t)
	t.Cleanup(func() { _ = db.Close() })
	start := time.Date(2026, 9, 12, 14, 0, 44, 0, time.UTC)
	end := start.Add(17 * time.Minute)
	bucket := start.Truncate(5 * time.Minute)
	mock.ExpectQuery(`(?s)SELECT avg\(viewer_count\), max\(viewer_count\).*window_start >= \? AND window_start < \?`).
		WithArgs("tenant-1", "stream-1", bucket, end).
		WillReturnRows(sqlmock.NewRows([]string{"avg", "peak"}).AddRow(6, 6))
	mock.ExpectQuery(`(?s)FROM periscope.stream_health_5m.*timestamp_5m >= \? AND timestamp_5m < \?`).
		WithArgs("tenant-1", "stream-1", bucket, end).
		WillReturnRows(sqlmock.NewRows([]string{"buffer", "bitrate", "fps", "rebuffer", "issues", "dry"}).AddRow(1, 100, 30, 0, 0, 0))
	mock.ExpectQuery(`(?s)FROM periscope.client_qoe_5m.*timestamp_5m >= \? AND timestamp_5m < \?`).
		WithArgs("tenant-1", "stream-1", bucket, end).
		WillReturnRows(sqlmock.NewRows([]string{"loss", "connection"}).AddRow(0, 1))
	mock.ExpectQuery(`(?s)SELECT.*sum\(seconds_observed\).*UNION ALL.*viewer_sessions_current`).
		WithArgs("tenant-1", "stream-1", bucket, end, start, end, "tenant-1", "stream-1", end, start, "tenant-1", "stream-1", bucket, end).
		WillReturnRows(sqlmock.NewRows([]string{"seconds", "bytes", "egress", "unique", "sessions"}).AddRow(60, 1000, 1000, 6, 6))
	mock.ExpectQuery(`(?s)FROM periscope.delivery_usage_5m_v`).
		WithArgs("tenant-1", "stream-1", bucket, end).
		WillReturnRows(sqlmock.NewRows([]string{"egress"}).AddRow(1000))
	mock.ExpectQuery(`(?s)SELECT uniqExact\(country_code\)`).
		WithArgs("tenant-1", "stream-1", end, start).
		WillReturnRows(sqlmock.NewRows([]string{"countries"}).AddRow(1))
	mock.ExpectQuery(`(?s)FROM periscope.quality_tier_daily`).
		WithArgs("tenant-1", "stream-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"2160", "1440", "1080", "720", "480", "sd", "264", "265", "vp9", "av1"}).AddRow(0, 0, 0, 0, 0, 0, 0, 0, 0, 0))
	resp, err := server.GetStreamAnalyticsSummary(context.Background(), &periscopepb.GetStreamAnalyticsSummaryRequest{
		TenantId: "tenant-1", StreamId: "stream-1", TimeRange: &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Summary.RangePeakConcurrentViewers != 6 || resp.Summary.RangeTotalSessions != 6 || resp.Summary.RangeEgressBytes != 1000 {
		t.Fatalf("partial bucket omitted: %v", resp.Summary)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
