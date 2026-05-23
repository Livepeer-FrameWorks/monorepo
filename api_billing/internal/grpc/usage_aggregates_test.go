package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGetUsageAggregatesBucketsMinuteFiveDeltaRows(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-usage-aggregate"
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	bucketStart := start
	bucketEnd := start.Add(time.Hour)

	mock.ExpectQuery(`WITH bucketed AS`).
		WithArgs(tenantID, start, end, "hourly").
		WillReturnRows(sqlmock.NewRows([]string{"usage_type", "period_start", "period_end", "usage_value", "granularity"}).
			AddRow("egress_gb", bucketStart, bucketEnd, 12.5, "hourly").
			AddRow("max_viewers", bucketStart, bucketEnd, 42.0, "hourly"))

	resp, err := server.GetUsageAggregates(context.Background(), &pb.GetUsageAggregatesRequest{
		TenantId:    tenantID,
		TimeRange:   &pb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
		Granularity: "hourly",
	})
	if err != nil {
		t.Fatalf("GetUsageAggregates: %v", err)
	}
	if len(resp.GetAggregates()) != 2 {
		t.Fatalf("aggregates len = %d, want 2", len(resp.GetAggregates()))
	}
	if got := resp.GetAggregates()[0].GetGranularity(); got != "hourly" {
		t.Fatalf("granularity = %q, want hourly", got)
	}
	if got := resp.GetAggregates()[0].GetPeriodStart().AsTime(); !got.Equal(bucketStart) {
		t.Fatalf("period_start = %v, want %v", got, bucketStart)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
