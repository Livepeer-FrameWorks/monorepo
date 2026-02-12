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
	"frameworks/pkg/pagination"
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

func TestGetCursorPagination(t *testing.T) {
	fixedTS := time.Unix(1730000000, 0).UTC()
	after := pagination.EncodeCursor(fixedTS, "event-1")
	before := pagination.EncodeCursor(fixedTS.Add(-time.Minute), "event-0")

	t.Run("nil request uses defaults", func(t *testing.T) {
		params, err := getCursorPagination(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if params.Limit != pagination.DefaultLimit {
			t.Fatalf("expected default limit %d, got %d", pagination.DefaultLimit, params.Limit)
		}
		if params.Direction != pagination.Forward {
			t.Fatalf("expected forward direction, got %v", params.Direction)
		}
		if params.Cursor != nil {
			t.Fatalf("expected nil cursor, got %#v", params.Cursor)
		}
	})

	t.Run("forward request parses first and after", func(t *testing.T) {
		params, err := getCursorPagination(&pb.CursorPaginationRequest{
			First: 25,
			After: &after,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if params.Limit != 25 {
			t.Fatalf("expected limit 25, got %d", params.Limit)
		}
		if params.Direction != pagination.Forward {
			t.Fatalf("expected forward direction, got %v", params.Direction)
		}
		if params.Cursor == nil {
			t.Fatal("expected parsed cursor")
		}
		if params.Cursor.ID != "event-1" {
			t.Fatalf("expected cursor id event-1, got %s", params.Cursor.ID)
		}
		if !params.Cursor.Timestamp.Equal(fixedTS) {
			t.Fatalf("expected cursor timestamp %s, got %s", fixedTS, params.Cursor.Timestamp)
		}
	})

	t.Run("backward request takes precedence over forward fields", func(t *testing.T) {
		params, err := getCursorPagination(&pb.CursorPaginationRequest{
			First:  100,
			After:  &after,
			Last:   5,
			Before: &before,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if params.Limit != 5 {
			t.Fatalf("expected limit 5 from last, got %d", params.Limit)
		}
		if params.Direction != pagination.Backward {
			t.Fatalf("expected backward direction, got %v", params.Direction)
		}
		if params.Cursor == nil || params.Cursor.ID != "event-0" {
			t.Fatalf("expected parsed before cursor id event-0, got %#v", params.Cursor)
		}
	})

	t.Run("invalid after cursor returns parse error", func(t *testing.T) {
		invalid := "not-base64"
		_, err := getCursorPagination(&pb.CursorPaginationRequest{
			First: 10,
			After: &invalid,
		})
		if err == nil {
			t.Fatal("expected error for invalid after cursor")
		}
		if !strings.Contains(err.Error(), "invalid after cursor") {
			t.Fatalf("expected invalid after cursor error, got %v", err)
		}
	})
}

func TestBuildCursorResponse(t *testing.T) {
	t.Run("forward response sets has next page from overfetch", func(t *testing.T) {
		resp := buildCursorResponse(11, 10, pagination.Forward, 120, "start", "end")
		if !resp.HasNextPage {
			t.Fatal("expected has_next_page=true when results are overfetched")
		}
		if !resp.HasPreviousPage {
			t.Fatal("expected has_previous_page=true when cursors are present")
		}
		if resp.TotalCount != 120 {
			t.Fatalf("expected total count 120, got %d", resp.TotalCount)
		}
		if resp.GetStartCursor() != "start" || resp.GetEndCursor() != "end" {
			t.Fatalf("unexpected cursors: start=%q end=%q", resp.GetStartCursor(), resp.GetEndCursor())
		}
	})

	t.Run("backward response flips page flags", func(t *testing.T) {
		resp := buildCursorResponse(6, 5, pagination.Backward, 42, "start", "end")
		if !resp.HasPreviousPage {
			t.Fatal("expected has_previous_page=true when results are overfetched")
		}
		if !resp.HasNextPage {
			t.Fatal("expected has_next_page=true when cursors are present")
		}
		if resp.TotalCount != 42 {
			t.Fatalf("expected total count 42, got %d", resp.TotalCount)
		}
	})

	t.Run("empty cursors stay unset", func(t *testing.T) {
		resp := buildCursorResponse(5, 5, pagination.Forward, 10, "", "")
		if resp.StartCursor != nil || resp.EndCursor != nil {
			t.Fatalf("expected nil cursors, got start=%v end=%v", resp.StartCursor, resp.EndCursor)
		}
		if resp.HasPreviousPage {
			t.Fatal("expected has_previous_page=false without cursors")
		}
	})
}

func TestBuildKeysetConditionN(t *testing.T) {
	ts := time.Unix(1730000000, 0).UTC()
	params := &pagination.Params{
		Direction: pagination.Forward,
		Cursor: &pagination.Cursor{
			Timestamp: ts,
			ID:        "row-1",
		},
	}

	t.Run("returns empty condition without cursor", func(t *testing.T) {
		noCursorParams := &pagination.Params{Direction: pagination.Forward}
		cond, args, err := buildKeysetConditionN(noCursorParams, "timestamp", []string{"stream_id"}, []string{"stream-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cond != "" {
			t.Fatalf("expected empty condition, got %q", cond)
		}
		if args != nil {
			t.Fatalf("expected nil args, got %#v", args)
		}
	})

	t.Run("returns invalid argument on tuple mismatch", func(t *testing.T) {
		_, _, err := buildKeysetConditionN(params, "timestamp", []string{"tenant_id", "stream_id"}, []string{"tenant-a"})
		if err == nil {
			t.Fatal("expected tuple mismatch error")
		}
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("expected invalid argument, got %v", err)
		}
	})

	t.Run("builds forward tuple condition", func(t *testing.T) {
		cond, args, err := buildKeysetConditionN(params, "timestamp", []string{"tenant_id", "stream_id"}, []string{"tenant-a", "stream-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := " AND (timestamp, tenant_id, stream_id) < (?, ?, ?)"
		if cond != want {
			t.Fatalf("expected %q, got %q", want, cond)
		}
		if len(args) != 3 {
			t.Fatalf("expected 3 args, got %d", len(args))
		}
		gotTS, ok := args[0].(time.Time)
		if !ok || !gotTS.Equal(ts) {
			t.Fatalf("expected timestamp arg %s, got %#v", ts, args[0])
		}
		if args[1] != "tenant-a" || args[2] != "stream-1" {
			t.Fatalf("unexpected tuple args: %#v", args[1:])
		}
	})

	t.Run("builds backward tuple condition", func(t *testing.T) {
		backward := &pagination.Params{
			Direction: pagination.Backward,
			Cursor: &pagination.Cursor{
				Timestamp: ts,
				ID:        "row-1",
			},
		}
		cond, _, err := buildKeysetConditionN(backward, "timestamp", []string{"stream_id"}, []string{"stream-1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := " AND (timestamp, stream_id) > (?, ?)"
		if cond != want {
			t.Fatalf("expected %q, got %q", want, cond)
		}
	})
}

func TestBuildOrderByN(t *testing.T) {
	t.Run("returns empty order by when no columns provided", func(t *testing.T) {
		orderBy := buildOrderByN(&pagination.Params{Direction: pagination.Forward}, "timestamp", nil)
		if orderBy != "" {
			t.Fatalf("expected empty order by, got %q", orderBy)
		}
	})

	t.Run("uses descending order for forward pagination", func(t *testing.T) {
		orderBy := buildOrderByN(&pagination.Params{Direction: pagination.Forward}, "timestamp", []string{"tenant_id", "stream_id"})
		want := " ORDER BY timestamp DESC, tenant_id DESC, stream_id DESC"
		if orderBy != want {
			t.Fatalf("expected %q, got %q", want, orderBy)
		}
	})

	t.Run("uses ascending order for backward pagination", func(t *testing.T) {
		orderBy := buildOrderByN(&pagination.Params{Direction: pagination.Backward}, "timestamp", []string{"tenant_id", "stream_id"})
		want := " ORDER BY timestamp ASC, tenant_id ASC, stream_id ASC"
		if orderBy != want {
			t.Fatalf("expected %q, got %q", want, orderBy)
		}
	})
}

func TestBuildKeysetCondition(t *testing.T) {
	ts := time.Unix(1730000000, 0).UTC()

	t.Run("no cursor returns empty condition", func(t *testing.T) {
		cond, args := buildKeysetCondition(&pagination.Params{}, "timestamp", "event_id")
		if cond != "" {
			t.Fatalf("expected empty condition, got %q", cond)
		}
		if args != nil {
			t.Fatalf("expected nil args, got %#v", args)
		}
	})

	t.Run("forward condition uses less-than tuple", func(t *testing.T) {
		cond, args := buildKeysetCondition(&pagination.Params{
			Direction: pagination.Forward,
			Cursor: &pagination.Cursor{
				Timestamp: ts,
				ID:        "event-1",
			},
		}, "timestamp", "event_id")
		if cond != " AND (timestamp, event_id) < (?, ?)" {
			t.Fatalf("unexpected condition: %q", cond)
		}
		if len(args) != 2 {
			t.Fatalf("expected 2 args, got %d", len(args))
		}
	})

	t.Run("backward condition uses greater-than tuple", func(t *testing.T) {
		cond, args := buildKeysetCondition(&pagination.Params{
			Direction: pagination.Backward,
			Cursor: &pagination.Cursor{
				Timestamp: ts,
				ID:        "event-1",
			},
		}, "timestamp", "event_id")
		if cond != " AND (timestamp, event_id) > (?, ?)" {
			t.Fatalf("unexpected condition: %q", cond)
		}
		if len(args) != 2 {
			t.Fatalf("expected 2 args, got %d", len(args))
		}
	})
}

func TestBuildOrderBy(t *testing.T) {
	forward := buildOrderBy(&pagination.Params{Direction: pagination.Forward}, "timestamp", "event_id")
	if forward != " ORDER BY timestamp DESC, event_id DESC" {
		t.Fatalf("unexpected forward order by: %q", forward)
	}

	backward := buildOrderBy(&pagination.Params{Direction: pagination.Backward}, "timestamp", "event_id")
	if backward != " ORDER BY timestamp ASC, event_id ASC" {
		t.Fatalf("unexpected backward order by: %q", backward)
	}
}

func TestBuildKeysetConditionSingle(t *testing.T) {
	ts := time.Unix(1730000000, 0).UTC()

	t.Run("no cursor returns empty condition", func(t *testing.T) {
		cond, args := buildKeysetConditionSingle(&pagination.Params{}, "timestamp")
		if cond != "" {
			t.Fatalf("expected empty condition, got %q", cond)
		}
		if args != nil {
			t.Fatalf("expected nil args, got %#v", args)
		}
	})

	t.Run("forward uses less-than", func(t *testing.T) {
		cond, args := buildKeysetConditionSingle(&pagination.Params{
			Direction: pagination.Forward,
			Cursor:    &pagination.Cursor{Timestamp: ts},
		}, "timestamp")
		if cond != " AND timestamp < ?" {
			t.Fatalf("unexpected condition: %q", cond)
		}
		if len(args) != 1 || args[0] != ts {
			t.Fatalf("unexpected args: %#v", args)
		}
	})

	t.Run("backward uses greater-than", func(t *testing.T) {
		cond, args := buildKeysetConditionSingle(&pagination.Params{
			Direction: pagination.Backward,
			Cursor:    &pagination.Cursor{Timestamp: ts},
		}, "timestamp")
		if cond != " AND timestamp > ?" {
			t.Fatalf("unexpected condition: %q", cond)
		}
		if len(args) != 1 || args[0] != ts {
			t.Fatalf("unexpected args: %#v", args)
		}
	})
}

func TestBuildOrderBySingle(t *testing.T) {
	forward := buildOrderBySingle(&pagination.Params{Direction: pagination.Forward}, "timestamp")
	if forward != " ORDER BY timestamp DESC" {
		t.Fatalf("unexpected forward order by: %q", forward)
	}

	backward := buildOrderBySingle(&pagination.Params{Direction: pagination.Backward}, "timestamp")
	if backward != " ORDER BY timestamp ASC" {
		t.Fatalf("unexpected backward order by: %q", backward)
	}
}

func TestCountAsync(t *testing.T) {
	t.Run("returns count on query success", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		t.Cleanup(func() {
			_ = db.Close()
		})

		server := &PeriscopeServer{
			clickhouse: db,
			logger:     logging.NewLoggerWithService("periscope-query-test"),
		}
		mock.ExpectQuery("SELECT count\\(\\*\\) FROM test_table WHERE tenant_id = \\?").
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(42)))

		ch := server.countAsync(context.Background(), "SELECT count(*) FROM test_table WHERE tenant_id = ?", "tenant-1")
		if got := <-ch; got != 42 {
			t.Fatalf("expected count 42, got %d", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet mock expectations: %v", err)
		}
	})

	t.Run("returns zero on query error", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("failed to create sqlmock: %v", err)
		}
		t.Cleanup(func() {
			_ = db.Close()
		})

		server := &PeriscopeServer{
			clickhouse: db,
			logger:     logging.NewLoggerWithService("periscope-query-test"),
		}
		mock.ExpectQuery("SELECT count\\(\\*\\) FROM test_table WHERE tenant_id = \\?").
			WithArgs("tenant-1").
			WillReturnError(errors.New("db error"))

		ch := server.countAsync(context.Background(), "SELECT count(*) FROM test_table WHERE tenant_id = ?", "tenant-1")
		if got := <-ch; got != 0 {
			t.Fatalf("expected count fallback 0, got %d", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet mock expectations: %v", err)
		}
	})
}

func TestJoinStrings(t *testing.T) {
	cases := []struct {
		name     string
		input    []string
		sep      string
		expected string
	}{
		{name: "empty", input: nil, sep: ",", expected: ""},
		{name: "single", input: []string{"a"}, sep: ",", expected: "a"},
		{name: "multiple", input: []string{"a", "b", "c"}, sep: "|", expected: "a|b|c"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinStrings(tc.input, tc.sep); got != tc.expected {
				t.Fatalf("joinStrings(%v, %q) = %q, want %q", tc.input, tc.sep, got, tc.expected)
			}
		})
	}
}

func TestNullInt64Value(t *testing.T) {
	cases := []struct {
		name     string
		input    sql.NullInt64
		expected int64
	}{
		{name: "invalid null", input: sql.NullInt64{Int64: 22, Valid: false}, expected: 0},
		{name: "valid value", input: sql.NullInt64{Int64: 22, Valid: true}, expected: 22},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nullInt64Value(tc.input); got != tc.expected {
				t.Fatalf("nullInt64Value(%+v) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}
