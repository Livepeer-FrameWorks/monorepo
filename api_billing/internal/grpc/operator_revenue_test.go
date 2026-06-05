package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGetOperatorRevenueExcludesHeldCredits(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "operator-tenant"
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(`status IN \('accruing', 'eligible', 'paid_out', 'clawed_back'\)`).
		WithArgs(tenantID, start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "currency", "gross_cents", "platform_fee_cents", "payable_cents", "line_count",
		}).AddRow("cluster-a", "EUR", int64(1000), int64(200), int64(800), int32(1)))

	resp, err := server.GetOperatorRevenue(context.Background(), &purserpb.GetOperatorRevenueRequest{
		TenantId:   tenantID,
		RangeStart: timestamppb.New(start),
		RangeEnd:   timestamppb.New(end),
	})
	if err != nil {
		t.Fatalf("GetOperatorRevenue: %v", err)
	}
	if got := resp.GetTotalPayableCents(); got != 800 {
		t.Fatalf("total payable cents = %d, want 800", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListOperatorClustersExcludesHeldCredits(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	server := &PurserServer{db: mockDB, logger: logging.NewLogger()}
	tenantID := "operator-tenant"

	mock.ExpectQuery(`status IN \('accruing', 'eligible', 'paid_out', 'clawed_back'\)`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "currency", "gross_cents", "platform_fee_cents", "payable_cents", "line_count",
		}).AddRow("cluster-a", "EUR", int64(1000), int64(200), int64(800), int32(1)))

	resp, err := server.ListOperatorClusters(context.Background(), &purserpb.ListOperatorClustersRequest{
		TenantId: tenantID,
	})
	if err != nil {
		t.Fatalf("ListOperatorClusters: %v", err)
	}
	if len(resp.GetClusters()) != 1 {
		t.Fatalf("clusters len = %d, want 1", len(resp.GetClusters()))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
