package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTenantActivityServer(t *testing.T) (*PeriscopeServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-tenant-activity-test"),
	}, mock
}

// Tenant-scoped (user JWT) callers must be rejected: the rollup is
// cross-tenant by design and only service credentials may read it.
func TestListTenantActivity_RejectsTenantScopedCallers(t *testing.T) {
	server, _ := newTenantActivityServer(t)

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
	_, err := server.ListTenantActivity(ctx, &periscopepb.ListTenantActivityRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for tenant-scoped caller, got %v", err)
	}

	ctx = context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	_, err = server.ListTenantActivity(ctx, &periscopepb.ListTenantActivityRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for user-scoped caller, got %v", err)
	}
}

func TestListTenantActivity_MergesRollupsAndSortsByViewerHours(t *testing.T) {
	server, mock := newTenantActivityServer(t)

	day := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

	// Q1: ingest hours per tenant from stream_runtime_daily.
	mock.ExpectQuery("FROM stream_runtime_daily").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "ingest_hours", "last_stream_day"}).
			AddRow("tenant-quiet", 1.5, day).
			AddRow("tenant-busy", 40.0, day))

	// Q2: viewer rollup from tenant_viewer_daily; only the busy tenant has viewers.
	mock.ExpectQuery("FROM tenant_viewer_daily").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "viewer_hours", "egress_gb", "unique_viewers", "total_sessions"}).
			AddRow("tenant-busy", 123.5, 42.25, int64(77), int64(900)))

	// Q3: API usage; only the quiet tenant called the API.
	mock.ExpectQuery("FROM api_usage_daily").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "requests", "errors"}).
			AddRow("tenant-quiet", int64(321), int64(4)))

	// Q4: live snapshot.
	mock.ExpectQuery("FROM stream_state_current FINAL").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "live_streams", "current_viewers"}).
			AddRow("tenant-busy", int32(2), int32(15)))

	resp, err := server.ListTenantActivity(context.Background(), &periscopepb.ListTenantActivityRequest{})
	if err != nil {
		t.Fatalf("ListTenantActivity: %v", err)
	}
	if len(resp.Tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(resp.Tenants))
	}

	busy := resp.Tenants[0]
	if busy.TenantId != "tenant-busy" {
		t.Fatalf("expected tenant-busy first (sorted by viewer hours), got %s", busy.TenantId)
	}
	if busy.ViewerHours != 123.5 || busy.EgressGb != 42.25 || busy.UniqueViewers != 77 || busy.TotalSessions != 900 {
		t.Fatalf("busy viewer rollup mismatch: %+v", busy)
	}
	if busy.IngestHours != 40.0 || busy.LiveStreams != 2 || busy.CurrentViewers != 15 {
		t.Fatalf("busy ingest/live mismatch: %+v", busy)
	}
	if busy.LastStreamAt == nil || !busy.LastStreamAt.AsTime().Equal(day) {
		t.Fatalf("busy last_stream_at mismatch: %v", busy.LastStreamAt)
	}

	quiet := resp.Tenants[1]
	if quiet.TenantId != "tenant-quiet" {
		t.Fatalf("expected tenant-quiet second, got %s", quiet.TenantId)
	}
	if quiet.ApiRequests != 321 || quiet.ApiErrors != 4 || quiet.IngestHours != 1.5 {
		t.Fatalf("quiet rollup mismatch: %+v", quiet)
	}

	if resp.TimeRange == nil || resp.GeneratedAt == nil {
		t.Fatalf("expected echoed time range and generated_at")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListTenantActivity_AppliesLimit(t *testing.T) {
	server, mock := newTenantActivityServer(t)

	day := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("FROM stream_runtime_daily").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "ingest_hours", "last_stream_day"}).
			AddRow("tenant-a", 5.0, day).
			AddRow("tenant-b", 9.0, day))
	mock.ExpectQuery("FROM tenant_viewer_daily").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "viewer_hours", "egress_gb", "unique_viewers", "total_sessions"}))
	mock.ExpectQuery("FROM api_usage_daily").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "requests", "errors"}))
	mock.ExpectQuery("FROM stream_state_current FINAL").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "live_streams", "current_viewers"}))

	resp, err := server.ListTenantActivity(context.Background(), &periscopepb.ListTenantActivityRequest{Limit: 1})
	if err != nil {
		t.Fatalf("ListTenantActivity: %v", err)
	}
	if len(resp.Tenants) != 1 || resp.Tenants[0].TenantId != "tenant-b" {
		t.Fatalf("expected only tenant-b (higher ingest hours), got %+v", resp.Tenants)
	}
}
