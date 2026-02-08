package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"frameworks/pkg/ctxkeys"

	"github.com/DATA-DOG/go-sqlmock"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestParseEventPayload(t *testing.T) {
	t.Run("empty payload", func(t *testing.T) {
		if got := parseEventPayload(""); got != nil {
			t.Fatal("expected nil for empty payload")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if got := parseEventPayload("not-json"); got != nil {
			t.Fatal("expected nil for invalid json")
		}
	})

	t.Run("valid json", func(t *testing.T) {
		payload := `{"viewer_id":"viewer-1","count":3}`
		got := parseEventPayload(payload)
		if got == nil {
			t.Fatal("expected struct for valid json")
		}
		if got.Fields["viewer_id"].GetStringValue() != "viewer-1" {
			t.Fatalf("unexpected viewer_id: %v", got.Fields["viewer_id"])
		}
		if got.Fields["count"].GetNumberValue() != 3 {
			t.Fatalf("unexpected count: %v", got.Fields["count"])
		}
	})
}

func TestValidateTimeRangeProto(t *testing.T) {
	t.Run("nil range defaults", func(t *testing.T) {
		start, end, err := validateTimeRangeProto(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if end.Before(start) {
			t.Fatal("expected end after start")
		}
		delta := end.Sub(start)
		if delta < 23*time.Hour || delta > 25*time.Hour {
			t.Fatalf("expected ~24h range, got %s", delta)
		}
	})

	t.Run("zero timestamps default", func(t *testing.T) {
		rangeProto := &pb.TimeRange{
			Start: timestamppb.New(time.Time{}),
			End:   timestamppb.New(time.Time{}),
		}
		start, end, err := validateTimeRangeProto(rangeProto)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !end.After(start) {
			t.Fatal("expected end after start")
		}
		if !strings.Contains(end.Sub(start).String(), "24h") {
			t.Fatalf("expected 24h range, got %s", end.Sub(start))
		}
	})

	t.Run("explicit timestamps", func(t *testing.T) {
		startTime := time.Now().Add(-2 * time.Hour).UTC()
		endTime := time.Now().Add(-time.Hour).UTC()
		rangeProto := &pb.TimeRange{
			Start: timestamppb.New(startTime),
			End:   timestamppb.New(endTime),
		}
		start, end, err := validateTimeRangeProto(rangeProto)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !start.Equal(startTime) {
			t.Fatalf("expected start %v, got %v", startTime, start)
		}
		if !end.Equal(endTime) {
			t.Fatalf("expected end %v, got %v", endTime, end)
		}
	})

	t.Run("end before start", func(t *testing.T) {
		startTime := time.Now().Add(-time.Hour).UTC()
		endTime := time.Now().Add(-2 * time.Hour).UTC()
		rangeProto := &pb.TimeRange{
			Start: timestamppb.New(startTime),
			End:   timestamppb.New(endTime),
		}
		_, _, err := validateTimeRangeProto(rangeProto)
		if err == nil {
			t.Fatal("expected error when end is before start")
		}
	})
}

func TestWrapClickhouseError(t *testing.T) {
	t.Run("deadline exceeded maps to gRPC deadline", func(t *testing.T) {
		err := wrapClickhouseError(context.DeadlineExceeded, "database error")
		st, ok := status.FromError(err)
		if !ok {
			t.Fatal("expected grpc status error")
		}
		if st.Code() != codes.DeadlineExceeded {
			t.Fatalf("expected deadline exceeded, got %s", st.Code())
		}
	})

	t.Run("bad connection maps to unavailable", func(t *testing.T) {
		err := wrapClickhouseError(sql.ErrConnDone, "database error")
		st, ok := status.FromError(err)
		if !ok {
			t.Fatal("expected grpc status error")
		}
		if st.Code() != codes.Unavailable {
			t.Fatalf("expected unavailable, got %s", st.Code())
		}
	})

	t.Run("generic error maps to internal", func(t *testing.T) {
		err := wrapClickhouseError(errors.New("boom"), "database error")
		st, ok := status.FromError(err)
		if !ok {
			t.Fatal("expected grpc status error")
		}
		if st.Code() != codes.Internal {
			t.Fatalf("expected internal, got %s", st.Code())
		}
	})
}

func TestGetLiveUsageSummaryPartialFailure(t *testing.T) {
	_, server, mock := newLiveUsageSummaryServer(t)

	setupLiveUsageSummaryMocks(t, mock, map[string]error{
		"FROM tenant_usage_5m": errors.New("query failed"),
	})

	resp, err := server.GetLiveUsageSummary(context.Background(), &pb.GetLiveUsageSummaryRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp == nil || resp.Summary == nil {
		t.Fatal("expected summary response")
	}
	if resp.Summary.UniqueUsers != 0 {
		t.Fatalf("expected fallback unique users to be 0, got %d", resp.Summary.UniqueUsers)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetLiveUsageSummaryTimeoutFallback(t *testing.T) {
	_, server, mock := newLiveUsageSummaryServer(t)

	setupLiveUsageSummaryMocks(t, mock, map[string]error{
		"FROM client_qoe_5m": context.DeadlineExceeded,
	})

	resp, err := server.GetLiveUsageSummary(context.Background(), &pb.GetLiveUsageSummaryRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if resp.Summary.PeakBandwidthMbps != 0 {
		t.Fatalf("expected peak bandwidth fallback to be 0, got %f", resp.Summary.PeakBandwidthMbps)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetLiveUsageSummaryAllQueriesFail(t *testing.T) {
	_, server, mock := newLiveUsageSummaryServer(t)

	setupLiveUsageSummaryMocks(t, mock, map[string]error{
		"FROM stream_event_log":         sql.ErrConnDone,
		"FROM tenant_usage_5m":          sql.ErrConnDone,
		"FROM client_qoe_5m":            sql.ErrConnDone,
		"FROM storage_usage_hourly":     sql.ErrConnDone,
		"FROM processing_daily":         sql.ErrConnDone,
		"FROM viewer_connection_events": sql.ErrConnDone,
		"FROM viewer_geo_hourly":        sql.ErrConnDone,
		"FROM artifact_events":          sql.ErrConnDone,
		"storage_scope = 'hot'":         sql.ErrConnDone,
		"storage_scope = 'cold'":        sql.ErrConnDone,
		"FROM storage_events":           sql.ErrConnDone,
	})

	_, err := server.GetLiveUsageSummary(context.Background(), &pb.GetLiveUsageSummaryRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
	})
	if err == nil {
		t.Fatal("expected error when all queries fail")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected grpc status error, got %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("expected unavailable, got %s", st.Code())
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func newLiveUsageSummaryServer(t *testing.T) (*sql.DB, *PeriscopeServer, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}
	return db, server, mock
}

func setupLiveUsageSummaryMocks(t *testing.T, mock sqlmock.Sqlmock, overrides map[string]error) {
	t.Helper()
	expectQuery := func(pattern string, columns []string, values []any) {
		if err, ok := overrides[pattern]; ok {
			mock.ExpectQuery(pattern).WillReturnError(err)
			return
		}

		// sqlmock expects []driver.Value, not []any.
		rowValues := make([]driver.Value, 0, len(values))
		for _, v := range values {
			rowValues = append(rowValues, v)
		}
		mock.ExpectQuery(pattern).WillReturnRows(sqlmock.NewRows(columns).AddRow(rowValues...))
	}

	expectQuery("FROM stream_event_log", []string{"max_viewers", "total_streams", "stream_hours"}, []any{int64(0), int64(0), float64(0)})
	expectQuery("FROM tenant_usage_5m", []string{"total_session_seconds", "total_bytes", "unique_viewers"}, []any{uint64(0), uint64(0), uint32(0)})
	expectQuery("FROM client_qoe_5m", []string{"peak_bandwidth"}, []any{float64(0)})
	expectQuery("FROM storage_usage_hourly", []string{"avg_total_bytes"}, []any{uint64(0)})
	expectQuery("FROM processing_daily", []string{
		"livepeer_h264", "livepeer_vp9", "livepeer_av1", "livepeer_hevc",
		"native_av_h264", "native_av_vp9", "native_av_av1", "native_av_hevc",
		"native_av_aac", "native_av_opus",
		"livepeer_segment_count", "native_av_segment_count",
		"livepeer_unique_streams", "native_av_unique_streams",
	}, []any{
		float64(0), float64(0), float64(0), float64(0),
		float64(0), float64(0), float64(0), float64(0),
		float64(0), float64(0),
		uint64(0), uint64(0),
		uint32(0), uint32(0),
	})
	expectQuery("FROM viewer_connection_events", []string{"unique_countries", "unique_cities"}, []any{int32(0), int32(0)})

	if err, ok := overrides["FROM viewer_geo_hourly"]; ok {
		mock.ExpectQuery("FROM viewer_geo_hourly").WillReturnError(err)
	} else {
		mock.ExpectQuery("FROM viewer_geo_hourly").WillReturnRows(
			sqlmock.NewRows([]string{"country_code", "viewer_count", "viewer_hours", "egress_gb"}),
		)
	}

	expectQuery("FROM artifact_events", []string{
		"clips_created", "clips_deleted", "dvr_created", "dvr_deleted", "vod_created", "vod_deleted",
	}, []any{uint32(0), uint32(0), uint32(0), uint32(0), uint32(0), uint32(0)})
	expectQuery("storage_scope = 'hot'", []string{"clip_bytes", "dvr_bytes", "vod_bytes"}, []any{uint64(0), uint64(0), uint64(0)})
	expectQuery("storage_scope = 'cold'", []string{"frozen_clip_bytes", "frozen_dvr_bytes", "frozen_vod_bytes"}, []any{uint64(0), uint64(0), uint64(0)})
	expectQuery("FROM storage_events", []string{"freeze_count", "freeze_bytes", "defrost_count", "defrost_bytes"}, []any{uint32(0), uint64(0), uint32(0), uint64(0)})
}

func TestRequireTenantID(t *testing.T) {
	t.Run("missing tenant context", func(t *testing.T) {
		_, err := requireTenantID(context.Background(), "")
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("uses context tenant when present", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		tenantID, err := requireTenantID(ctx, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tenantID != "tenant-a" {
			t.Fatalf("expected tenant-a, got %s", tenantID)
		}
	})

	t.Run("rejects mismatched tenant id", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		_, err := requireTenantID(ctx, "tenant-b")
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected permission denied, got %v", err)
		}
	})

	t.Run("allows request tenant for service calls", func(t *testing.T) {
		tenantID, err := requireTenantID(context.Background(), "tenant-service")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tenantID != "tenant-service" {
			t.Fatalf("expected tenant-service, got %s", tenantID)
		}
	})
}

func TestValidateRelatedTenantIDs(t *testing.T) {
	t.Run("allows empty related list", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		if err := validateRelatedTenantIDs(ctx, nil); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects related list for authenticated tenant", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
		err := validateRelatedTenantIDs(ctx, []string{"tenant-b"})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected permission denied, got %v", err)
		}
	})

	t.Run("allows related list for service calls", func(t *testing.T) {
		if err := validateRelatedTenantIDs(context.Background(), []string{"tenant-b"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
