package grpc

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGetTenantAnalyticsDailyCursorUsesProjectedDayAlias(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	mock.MatchExpectationsInOrder(false)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	after := pagination.EncodeCursor(start.Add(24*time.Hour), "")
	mock.ExpectQuery(`(?s)SELECT count\(\).*viewer_sessions_final_v`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`(?s)AS result_day.*FULL OUTER JOIN viewer.*AND result_day < \?.*ORDER BY result_day DESC`).
		WillReturnRows(sqlmock.NewRows([]string{
			"day", "tenant_id", "total_streams", "total_views", "unique_viewers", "egress_bytes",
		}))
	_, err := server.GetTenantAnalyticsDaily(serviceTestContext(), &periscopepb.GetTenantAnalyticsDailyRequest{
		TenantId:   "tenant-a",
		TimeRange:  &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
		Pagination: &commonpb.CursorPaginationRequest{First: 1, After: &after},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetStreamAnalyticsDailyReadsDeliveryBackedRollup(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	mock.MatchExpectationsInOrder(false)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	day := start
	const streamID = "11111111-1111-4111-8111-111111111111"

	mock.ExpectQuery(`(?s)WITH daily AS .*FROM stream_analytics_daily.*SELECT count\(\).*stream_id = \?`).
		WithArgs("tenant-a", start, end, streamID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	mock.ExpectQuery(`(?s)SELECT.*egress_bytes.*FROM stream_analytics_daily.*stream_id = \?`).
		WithArgs("tenant-a", start, end, streamID).
		WillReturnRows(sqlmock.NewRows([]string{
			"day", "tenant_id", "stream_id", "total_views", "unique_viewers",
			"unique_countries", "unique_cities", "egress_bytes",
		}).AddRow(day, "tenant-a", streamID, int64(0), int32(0), int32(0), int32(0), int64(3<<30)))

	resp, err := server.GetStreamAnalyticsDaily(serviceTestContext(), &periscopepb.GetStreamAnalyticsDailyRequest{
		TenantId: "tenant-a",
		StreamId: proto.String(streamID),
		TimeRange: &commonpb.TimeRange{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetRecords()) != 1 || resp.GetRecords()[0].GetEgressBytes() != int64(3<<30) || resp.GetRecords()[0].GetTotalViews() != 0 {
		t.Fatalf("delivery-backed stream daily row mismatch: %+v", resp.GetRecords())
	}
	if resp.GetPagination().GetTotalCount() != 1 {
		t.Fatalf("rollup count mismatch: %+v", resp.GetPagination())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetStreamAnalyticsDailyUsesHalfOpenDayRange(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	mock.MatchExpectationsInOrder(false)
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery(`(?s)WITH daily AS .*day < toDate\(\?\).*SELECT count\(\)`).
		WithArgs("tenant-a", start.Truncate(24*time.Hour), end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(0)))
	mock.ExpectQuery(`(?s)SELECT.*FROM stream_analytics_daily.*day < toDate\(\?\)`).
		WithArgs("tenant-a", start.Truncate(24*time.Hour), end).
		WillReturnRows(sqlmock.NewRows([]string{
			"day", "tenant_id", "stream_id", "total_views", "unique_viewers",
			"unique_countries", "unique_cities", "egress_bytes",
		}))

	if _, err := server.GetStreamAnalyticsDaily(serviceTestContext(), &periscopepb.GetStreamAnalyticsDailyRequest{
		TenantId:  "tenant-a",
		TimeRange: &commonpb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
