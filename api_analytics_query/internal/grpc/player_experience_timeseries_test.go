package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func newTimeSeriesServer(t *testing.T) (*PeriscopeServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}
	return server, mock, func() { _ = db.Close() }
}

func fixedTimeRange() *commonpb.TimeRange {
	return &commonpb.TimeRange{
		Start: timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)),
		End:   timestamppb.New(time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)),
	}
}

// The boot time-series buckets at the requested interval, computes percentiles per
// window, and uses a half-open [start, end) range so boundary samples land once.
func TestGetPlayerBootTimeSeriesBucketsHalfOpen(t *testing.T) {
	server, mock, cleanup := newTimeSeriesServer(t)
	defer cleanup()

	bucket := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)toStartOfInterval\(timestamp, INTERVAL 15 MINUTE\).*quantileIf\(0\.95\)\(total_ttf_ms, total_ttf_ms > 0\).*FROM player_boot_samples.*timestamp >= \? AND timestamp < \?.*GROUP BY bucket`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "boot_count", "p50", "p95", "p99"}).
			AddRow(bucket, int64(42), 800.0, 1950.0, 3400.0))

	resp, err := server.GetPlayerBootTimeSeries(context.Background(), &periscopepb.GetPlayerBootTimeSeriesRequest{
		TenantId:  "tenant-1",
		Interval:  "15m",
		TimeRange: fixedTimeRange(),
	})
	if err != nil {
		t.Fatalf("GetPlayerBootTimeSeries: %v", err)
	}
	if len(resp.GetBuckets()) != 1 {
		t.Fatalf("buckets = %d, want 1", len(resp.GetBuckets()))
	}
	b := resp.GetBuckets()[0]
	if b.GetBootCount() != 42 || b.GetP95TtfMs() != 1950 {
		t.Fatalf("bucket = %+v, want bootCount=42 p95=1950", b)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet mock expectations: %v", err)
	}
}

// The QoE time-series rolls deltas up per (bucket, session) first, then computes
// per-bucket ratios — preserving the scalar summary's sum-of-deltas semantics —
// over a half-open range.
func TestGetSessionQoeTimeSeriesInnerSessionRollup(t *testing.T) {
	server, mock, cleanup := newTimeSeriesServer(t)
	defer cleanup()

	bucket := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)FROM \(.*GROUP BY bucket, content_id, session_id.*\).*GROUP BY bucket`).
		WillReturnRows(sqlmock.NewRows([]string{"bucket", "session_count", "played_hours", "rebuffering_ratio", "frame_drop_ratio", "avg_bitrate_bps"}).
			AddRow(bucket, int64(120), 18.5, 0.0061, 0.0009, 4_350_000.0))

	resp, err := server.GetSessionQoeTimeSeries(context.Background(), &periscopepb.GetSessionQoeTimeSeriesRequest{
		TenantId:  "tenant-1",
		Interval:  "1h",
		TimeRange: fixedTimeRange(),
	})
	if err != nil {
		t.Fatalf("GetSessionQoeTimeSeries: %v", err)
	}
	if len(resp.GetBuckets()) != 1 {
		t.Fatalf("buckets = %d, want 1", len(resp.GetBuckets()))
	}
	b := resp.GetBuckets()[0]
	if b.GetSessionCount() != 120 || b.GetRebufferingRatio() != 0.0061 {
		t.Fatalf("bucket = %+v, want sessionCount=120 rebufRatio=0.0061", b)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet mock expectations: %v", err)
	}
}

// The retention-asset list is gated to retained artifact content with a real
// reach sample over a half-open range; clips and VODs both have retention
// curves.
func TestListVodRetentionAssetsEligibilityGate(t *testing.T) {
	server, mock, cleanup := newTimeSeriesServer(t)
	defer cleanup()
	// count + main queries run concurrently (countAsync), so don't enforce order.
	mock.MatchExpectationsInOrder(false)

	mock.ExpectQuery(`(?s)uniqExact\(artifact_hash\).*content_type IN \('vod', 'clip'\) AND bucket_width_s > 0.*timestamp >= \? AND timestamp < \?`).
		WillReturnRows(sqlmock.NewRows([]string{"cnt"}).AddRow(int32(1)))

	lastSeen := time.Date(2026, 6, 1, 18, 30, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)content_type IN \('vod', 'clip'\) AND bucket_width_s > 0.*timestamp >= \? AND timestamp < \?.*GROUP BY artifact_hash`).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash", "total_sessions", "duration_s", "last_seen"}).
			AddRow("hash-abc", int64(2410), int32(3600), lastSeen))

	resp, err := server.ListVodRetentionAssets(context.Background(), &periscopepb.ListVodRetentionAssetsRequest{
		TenantId:  "tenant-1",
		TimeRange: fixedTimeRange(),
	})
	if err != nil {
		t.Fatalf("ListVodRetentionAssets: %v", err)
	}
	if len(resp.GetAssets()) != 1 {
		t.Fatalf("assets = %d, want 1", len(resp.GetAssets()))
	}
	a := resp.GetAssets()[0]
	if a.GetArtifactHash() != "hash-abc" || a.GetTotalSessions() != 2410 || a.GetDurationS() != 3600 {
		t.Fatalf("asset = %+v, want hash-abc/2410/3600", a)
	}
	if resp.GetPagination().GetTotalCount() != 1 {
		t.Fatalf("totalCount = %d, want 1", resp.GetPagination().GetTotalCount())
	}
	if resp.GetPagination().GetHasPreviousPage() {
		t.Fatal("hasPreviousPage = true on first page, want false")
	}
	if resp.GetPagination().GetHasNextPage() {
		t.Fatal("hasNextPage = true for single-row page, want false")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet mock expectations: %v", err)
	}
}
