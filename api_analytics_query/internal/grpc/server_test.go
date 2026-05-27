package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

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

func TestLookupLiveIntervalStartsPrefersCurrentStateStart(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}
	streamID := uuid.New()
	startedAt := time.Date(2026, 5, 27, 17, 43, 7, 0, time.UTC)

	mock.ExpectQuery(`(?s)if\(\s*ifNull\(s\.started_at, toDateTime\(0\)\) > ifNull\(last_end\.ended_at, toDateTime\(0\)\).*FROM periscope\.stream_state_current AS s FINAL`).
		WithArgs("tenant-1", streamID, "tenant-1", streamID).
		WillReturnRows(sqlmock.NewRows([]string{"stream_id", "started_at"}).
			AddRow(streamID.String(), startedAt))

	got := server.lookupLiveIntervalStarts(context.Background(), "tenant-1", []string{streamID.String()})
	if !got[streamID.String()].Equal(startedAt) {
		t.Fatalf("started_at = %v, want %v", got[streamID.String()], startedAt)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetLiveUsageSummaryPartialFailureFailsClosed(t *testing.T) {
	_, server, mock := newLiveUsageSummaryServer(t)

	setupLiveUsageSummaryMocks(t, mock, map[string]error{
		liveViewerUsagePattern: errors.New("query failed"),
	})

	_, err := server.GetLiveUsageSummary(context.Background(), &pb.GetLiveUsageSummaryRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
	})
	if err == nil {
		t.Fatal("expected error on partial live usage source failure")
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetLiveUsageSummaryTimeoutFailsClosed(t *testing.T) {
	_, server, mock := newLiveUsageSummaryServer(t)

	setupLiveUsageSummaryMocks(t, mock, map[string]error{
		"FROM client_qoe_5m": context.DeadlineExceeded,
	})

	_, err := server.GetLiveUsageSummary(context.Background(), &pb.GetLiveUsageSummaryRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
	})
	if err == nil {
		t.Fatal("expected error on live usage timeout")
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetLiveUsageSummaryAllQueriesFail(t *testing.T) {
	_, server, mock := newLiveUsageSummaryServer(t)

	setupLiveUsageSummaryMocks(t, mock, map[string]error{
		liveRuntimeSummaryPattern:      sql.ErrConnDone,
		liveViewerUsagePattern:         sql.ErrConnDone,
		"FROM client_qoe_5m":           sql.ErrConnDone,
		"FROM storage_gb_seconds_5m_v": sql.ErrConnDone,
		"FROM processing_5m_v":         sql.ErrConnDone,
		liveGeoSummaryPattern:          sql.ErrConnDone,
		liveGeoBreakdownPattern:        sql.ErrConnDone,
		"FROM artifact_events":         sql.ErrConnDone,
		"storage_scope = 'hot'":        sql.ErrConnDone,
		"storage_scope = 'cold'":       sql.ErrConnDone,
		"FROM storage_events":          sql.ErrConnDone,
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

func TestGetNetworkLiveStatsEgressCapacity(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}

	// Two clusters:
	//   edge-a: one healthy/normal node (counted) + one maintenance node (load
	//   counted, capacity excluded) + one unhealthy node (capacity excluded).
	//   edge-b: one healthy/normal node only.
	// Aggregated by ClickHouse, the result the resolver scans is the per-cluster
	// row: (cluster_id, sum(up_speed), sum(down_speed), sumIf(bw_limit,...), countIf(is_healthy=1)).
	mock.ExpectQuery(`(?s)sumIf\(bw_limit, is_healthy = 1 AND operational_mode = 'normal'\).*FROM periscope\.node_state_current FINAL`).WillReturnRows(
		sqlmock.NewRows([]string{"cluster_id", "up", "down", "egress_capacity_bps", "active_nodes"}).
			// edge-a: up 700+200+50, down 100+30+5, capacity only from the healthy normal node (1.25 Gbps)
			AddRow("edge-a", uint64(950), uint64(135), uint64(1_250_000_000), int32(2)).
			// edge-b: single healthy normal 10 Gbps node, 500 up / 50 down
			AddRow("edge-b", uint64(500), uint64(50), uint64(10_000_000_000), int32(1)),
	)
	mock.ExpectQuery(`FROM periscope\.stream_state_current`).WillReturnRows(
		sqlmock.NewRows([]string{"cluster_id", "streams", "viewers"}).
			AddRow("edge-a", int32(3), int32(120)).
			AddRow("edge-b", int32(1), int32(40)),
	)

	resp, err := server.GetNetworkLiveStats(context.Background(), &pb.GetNetworkLiveStatsRequest{})
	if err != nil {
		t.Fatalf("GetNetworkLiveStats: %v", err)
	}

	got := map[string]*pb.NetworkClusterLiveStats{}
	for _, c := range resp.Clusters {
		got[c.ClusterId] = c
	}

	a, ok := got["edge-a"]
	if !ok {
		t.Fatalf("missing edge-a in response")
	}
	if a.EgressCapacityBps != 1_250_000_000 {
		t.Errorf("edge-a egress capacity = %d, want 1_250_000_000 (maintenance + unhealthy nodes excluded)", a.EgressCapacityBps)
	}
	if a.UploadBytesPerSec != 950 || a.DownloadBytesPerSec != 135 {
		t.Errorf("edge-a up/down = %d/%d, want 950/135 (all nodes' load aggregated)", a.UploadBytesPerSec, a.DownloadBytesPerSec)
	}
	if a.ActiveStreams != 3 || a.CurrentViewers != 120 {
		t.Errorf("edge-a streams/viewers = %d/%d, want 3/120", a.ActiveStreams, a.CurrentViewers)
	}

	b, ok := got["edge-b"]
	if !ok {
		t.Fatalf("missing edge-b in response")
	}
	if b.EgressCapacityBps != 10_000_000_000 {
		t.Errorf("edge-b egress capacity = %d, want 10_000_000_000", b.EgressCapacityBps)
	}

	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetGeographicDistributionEmptyKnownGeo(t *testing.T) {
	db, server, mock := newLiveUsageSummaryServer(t)
	defer db.Close()

	mock.ExpectQuery(`SELECT country_code, uniqExact\(node_id, session_id\) as cnt[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"country_code", "cnt"}))
	mock.ExpectQuery(`SELECT uniqExact\(node_id, session_id\)[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"total"}).AddRow(uint64(0)))
	mock.ExpectQuery(`SELECT city, country_code, uniqExact\(node_id, session_id\) as cnt,[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"city", "country_code", "cnt", "lat", "lon"}))
	mock.ExpectQuery(`SELECT uniqExact\(city, country_code\)[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"unique_cities"}).AddRow(uint64(0)))
	mock.ExpectQuery(`SELECT uniqExact\(country_code\), uniqExact\(node_id, session_id\)[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"unique_countries", "total_viewers"}).AddRow(uint64(0), uint64(0)))

	resp, err := server.GetGeographicDistribution(context.Background(), &pb.GetGeographicDistributionRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
		TopN: 10,
	})
	if err != nil {
		t.Fatalf("GetGeographicDistribution: %v", err)
	}
	if len(resp.GetTopCountries()) != 0 || len(resp.GetTopCities()) != 0 {
		t.Fatalf("expected empty known-geo distribution, got countries=%d cities=%d", len(resp.GetTopCountries()), len(resp.GetTopCities()))
	}
	if resp.GetTotalViewers() != 0 || resp.GetUniqueCountries() != 0 || resp.GetUniqueCities() != 0 {
		t.Fatalf("expected zero counts, got total=%d countries=%d cities=%d", resp.GetTotalViewers(), resp.GetUniqueCountries(), resp.GetUniqueCities())
	}

	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetGeographicDistributionUsesCurrentSessionOverlap(t *testing.T) {
	db, server, mock := newLiveUsageSummaryServer(t)
	defer db.Close()

	mock.ExpectQuery(`SELECT country_code, uniqExact\(node_id, session_id\) as cnt[\s\S]*FROM periscope\.viewer_sessions_current FINAL[\s\S]*ifNull\(connected_at, disconnected_at\) <= \? AND ifNull\(disconnected_at, \?\) >= \?`).
		WillReturnRows(sqlmock.NewRows([]string{"country_code", "cnt"}).
			AddRow("NL", uint64(3)).
			AddRow("DE", uint64(1)))
	mock.ExpectQuery(`SELECT uniqExact\(node_id, session_id\)[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"total"}).AddRow(uint64(4)))
	mock.ExpectQuery(`SELECT city, country_code, uniqExact\(node_id, session_id\) as cnt,[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"city", "country_code", "cnt", "lat", "lon"}).
			AddRow("The Hague", "NL", uint64(3), float64(52.0705), float64(4.3007)))
	mock.ExpectQuery(`SELECT uniqExact\(city, country_code\)[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"unique_cities"}).AddRow(uint64(1)))
	mock.ExpectQuery(`SELECT uniqExact\(country_code\), uniqExact\(node_id, session_id\)[\s\S]*FROM periscope\.viewer_sessions_current FINAL`).
		WillReturnRows(sqlmock.NewRows([]string{"unique_countries", "total_viewers"}).AddRow(uint64(2), uint64(4)))

	resp, err := server.GetGeographicDistribution(context.Background(), &pb.GetGeographicDistributionRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(time.Now().Add(-24 * time.Hour)),
			End:   timestamppb.New(time.Now()),
		},
		TopN: 10,
	})
	if err != nil {
		t.Fatalf("GetGeographicDistribution: %v", err)
	}
	if resp.GetTotalViewers() != 4 || resp.GetUniqueCountries() != 2 || resp.GetUniqueCities() != 1 {
		t.Fatalf("unexpected totals: total=%d countries=%d cities=%d", resp.GetTotalViewers(), resp.GetUniqueCountries(), resp.GetUniqueCities())
	}
	if got := resp.GetTopCountries(); len(got) != 2 || got[0].GetCountryCode() != "NL" || got[0].GetViewerCount() != 3 {
		t.Fatalf("unexpected top countries: %#v", got)
	}
	if got := resp.GetTopCities(); len(got) != 1 || got[0].GetCity() != "The Hague" || got[0].GetViewerCount() != 3 {
		t.Fatalf("unexpected top cities: %#v", got)
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

const (
	liveRuntimeSummaryPattern = `sum\(active_seconds\)[\s\S]*FROM periscope\.stream_runtime_5m_v`
	liveViewerUsagePattern    = `FROM viewer_usage_5m_v[\s\S]*UNION ALL[\s\S]*FROM viewer_sessions_current FINAL`
	liveGeoSummaryPattern     = `uniqExactIf\(country_code[\s\S]*FROM viewer_sessions_current FINAL`
	liveGeoBreakdownPattern   = `SELECT[\s\S]*country_code[\s\S]*viewer_count[\s\S]*FROM viewer_sessions_current FINAL`
)

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

	expectQuery(liveRuntimeSummaryPattern, []string{"stream_hours", "peak_concurrent", "total_streams"}, []any{float64(0), int32(0), int32(0)})
	expectQuery(liveViewerUsagePattern, []string{"total_session_seconds", "egress_bytes", "total_viewers", "unique_viewers"}, []any{uint64(0), uint64(0), uint32(0), uint32(0)})
	expectQuery("FROM client_qoe_5m", []string{"peak_bandwidth"}, []any{float64(0)})
	expectQuery("FROM storage_gb_seconds_5m_v", []string{"gb_seconds"}, []any{float64(0)})
	expectQuery("FROM processing_5m_v", []string{
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
	expectQuery(liveGeoSummaryPattern, []string{"unique_countries", "unique_cities"}, []any{int32(0), int32(0)})

	if err, ok := overrides[liveGeoBreakdownPattern]; ok {
		mock.ExpectQuery(liveGeoBreakdownPattern).WillReturnError(err)
	} else {
		mock.ExpectQuery(liveGeoBreakdownPattern).WillReturnRows(
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

func TestGetAPIUsageCursorPredicateStaysInWhere(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)

	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}

	start := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	operationName := "GetStream"
	after := pagination.EncodeCursor(start.Add(30*time.Minute), "jwt|query|GetStream")

	mock.ExpectQuery(`(?s)SELECT operation_type,.*FROM api_usage_5m_v.*ifNull\(operation_name, ''\) = \?.*GROUP BY operation_type`).
		WillReturnRows(sqlmock.NewRows([]string{
			"operation_type", "total_requests", "total_errors", "total_duration_ms", "total_complexity", "unique_operations",
		}).AddRow("query", uint64(1), uint64(0), uint64(42), uint64(7), uint64(1)))

	mock.ExpectQuery(`(?s)SELECT count\(\) FROM \(\s*SELECT window_start, auth_type, operation_type, operation_name\s*FROM api_usage_5m_v.*GROUP BY window_start, auth_type, operation_type, operation_name\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(0)))

	mock.ExpectQuery(`(?s)FROM api_usage_5m_v\s+WHERE tenant_id = \? AND window_start >= \? AND window_start < \?.*AND \(window_start, auth_type, operation_type, ifNull\(operation_name, ''\)\) < \(\?, \?, \?, \?\).*GROUP BY hour, tenant_id, auth_type, operation_type, operation_name`).
		WillReturnRows(sqlmock.NewRows([]string{
			"hour", "tenant_id", "auth_type", "operation_type", "operation_name",
			"total_requests", "total_errors", "total_duration_ms", "total_complexity",
			"unique_users", "unique_tokens",
		}))

	_, err = server.GetAPIUsage(context.Background(), &pb.GetAPIUsageRequest{
		TenantId:      "tenant-1",
		OperationName: &operationName,
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		},
		Pagination: &pb.CursorPaginationRequest{
			First: 1,
			After: &after,
		},
	})
	if err != nil {
		t.Fatalf("GetAPIUsage: %v", err)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetProcessingUsageSummariesCountDistinctSegments(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}

	start := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	mock.ExpectQuery(`(?s)uniqExactIf\(source_event_id, process_type = 'Livepeer'\).*uniqExactIf\(stream_id, process_type = 'Livepeer'\).*FROM processing_5m_v`).
		WillReturnRows(sqlmock.NewRows([]string{
			"day", "tenant_id", "livepeer_seconds", "livepeer_segment_count", "livepeer_unique_streams",
			"livepeer_h264", "livepeer_vp9", "livepeer_av1", "livepeer_hevc",
			"native_av_seconds", "native_av_segment_count", "native_av_unique_streams",
			"native_av_h264", "native_av_vp9", "native_av_av1", "native_av_hevc",
			"native_av_aac", "native_av_opus", "audio_seconds", "video_seconds",
		}))

	_, err = server.GetProcessingUsage(context.Background(), &pb.GetProcessingUsageRequest{
		TenantId:    "tenant-1",
		SummaryOnly: true,
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		},
	})
	if err != nil {
		t.Fatalf("GetProcessingUsage: %v", err)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetPlatformOverviewUsesCanonicalLedgers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}

	start := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	mock.ExpectQuery(`(?s)FROM stream_state_current FINAL\s+WHERE tenant_id = \?`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"total_streams", "active_streams", "total_viewers", "avg_viewers", "total_upload_bytes", "total_download_bytes",
		}).AddRow(int32(2), int32(1), int32(10), float64(5), uint64(100), uint64(200)))
	mock.ExpectQuery(`(?s)FROM client_qoe_5m\s+WHERE tenant_id = \?.*timestamp_5m >= \?.*timestamp_5m <\s+\?`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"peak_bandwidth"}).AddRow(float64(123)))
	mock.ExpectQuery(`(?s)FROM periscope\.viewer_usage_5m_v AS u.*WHERE u\.tenant_id = \?.*u\.window_start >= \?.*u\.window_start <\s+\?`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"egress_gb", "viewer_hours", "unique_viewers", "total_views", "peak_viewers"}).
			AddRow(float64(1.5), float64(2.0), int64(3), int64(4), int64(6)))
	mock.ExpectQuery(`(?s)sum\(active_seconds\).*FROM periscope\.stream_runtime_5m_v.*UNION ALL.*FROM periscope\.stream_state_current AS s FINAL`).
		WithArgs("tenant-1", start, end, start, end, "tenant-1", "tenant-1", end, start).
		WillReturnRows(sqlmock.NewRows([]string{"stream_hours", "peak_concurrent", "total_streams"}).AddRow(float64(9), int32(8), int32(2)))

	resp, err := server.GetPlatformOverview(context.Background(), &pb.GetPlatformOverviewRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		},
	})
	if err != nil {
		t.Fatalf("GetPlatformOverview: %v", err)
	}
	if resp.TotalViews != 4 || resp.UniqueViewers != 3 || resp.PeakViewers != 6 || resp.PeakConcurrentViewers != 8 {
		t.Fatalf("unexpected overview viewer metrics: %+v", resp)
	}
	if resp.StreamHours != 9 || resp.IngestHours != 9 || resp.ViewerHours != 2 || resp.EgressGb != 1.5 {
		t.Fatalf("unexpected overview usage metrics: %+v", resp)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
}

func TestGetStreamAnalyticsSummariesUsesViewerUsageLedger(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	server := &PeriscopeServer{
		clickhouse: db,
		logger:     logging.NewLoggerWithService("periscope-query-test"),
	}

	start := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	mock.ExpectQuery(`(?s)WITH stream_totals AS.*FROM periscope\.viewer_usage_5m_v.*WHERE tenant_id = \? AND window_start >= \? AND window_start < \?.*ORDER BY egress_bytes DESC`).
		WithArgs("tenant-1", start, end, 2).
		WillReturnRows(sqlmock.NewRows([]string{
			"stream_id", "total_views", "unique_viewers", "egress_bytes", "viewer_seconds",
			"egress_gb", "viewer_hours", "views_share_pct", "viewers_share_pct", "egress_share_pct", "viewer_hours_share_pct",
		}).AddRow("stream-a", int64(4), int64(3), int64(1073741824), int64(7200), float64(1), float64(2), float64(100), float64(100), float64(100), float64(100)))
	mock.ExpectQuery(`(?s)SELECT count\(DISTINCT stream_id\)\s+FROM periscope\.viewer_usage_5m_v\s+WHERE tenant_id = \? AND window_start >= \? AND window_start < \?`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))

	resp, err := server.GetStreamAnalyticsSummaries(context.Background(), &pb.GetStreamAnalyticsSummariesRequest{
		TenantId: "tenant-1",
		TimeRange: &pb.TimeRange{
			Start: timestamppb.New(start),
			End:   timestamppb.New(end),
		},
		Pagination: &pb.CursorPaginationRequest{First: 1},
	})
	if err != nil {
		t.Fatalf("GetStreamAnalyticsSummaries: %v", err)
	}
	if resp.TotalCount != 1 || len(resp.Summaries) != 1 {
		t.Fatalf("unexpected summary result: %+v", resp)
	}
	got := resp.Summaries[0]
	if got.RangeTotalViews != 4 || got.RangeUniqueViewers != 3 || got.RangeEgressBytes != 1073741824 || got.RangeViewerSeconds != 7200 {
		t.Fatalf("unexpected summary metrics: %+v", got)
	}
	if mockErr := mock.ExpectationsWereMet(); mockErr != nil {
		t.Fatalf("unmet mock expectations: %v", mockErr)
	}
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

func TestBuildOrchestratorVantagesQueryKeepsLastValidGeo(t *testing.T) {
	query, args := buildOrchestratorVantagesQuery("tenant-a", "")
	for _, want := range []string{
		"FROM periscope.orchestrator_vantage_current FINAL",
		"FROM periscope.orchestrator_discovery_samples",
		"geo_source != 'unknown'",
		"argMax(geo_source, timestamp) AS latest_geo_source",
		"coalesce(geo.geo_latitude, latest.latitude)",
		"coalesce(geo.geo_longitude, latest.longitude)",
		"latest.latest_latency_ms",
		"latest.dialed_recently",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
	if len(args) != 2 || args[0] != "tenant-a" || args[1] != "tenant-a" {
		t.Fatalf("args = %#v, want tenant for latest and geo subqueries", args)
	}
	if strings.Contains(query, "argMax(geo_source, timestamp) AS geo_source") {
		t.Fatalf("query reuses geo_source as an aggregate alias:\n%s", query)
	}
}

func TestBuildOrchestratorVantagesQueryFiltersOrchInLatestAndGeo(t *testing.T) {
	query, args := buildOrchestratorVantagesQuery("tenant-a", "orch-a")
	if got := strings.Count(query, "AND orch_addr = ?"); got != 2 {
		t.Fatalf("orch filter count = %d, want 2:\n%s", got, query)
	}
	wantArgs := []any{"tenant-a", "orch-a", "tenant-a", "orch-a"}
	if len(args) != len(wantArgs) {
		t.Fatalf("args = %#v, want %#v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Fatalf("args = %#v, want %#v", args, wantArgs)
		}
	}
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

func TestGetFederationEvents_IncludesGeoCoordinates(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	server := &PeriscopeServer{clickhouse: db, logger: logging.NewLoggerWithService("periscope-query-test")}

	start := time.Now().Add(-time.Hour).UTC()
	end := time.Now().UTC()
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")

	mock.ExpectQuery(`SELECT count\(\) FROM periscope.federation_events`).
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(1)))

	mock.ExpectQuery("FROM periscope.federation_events").
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), int32(100)).
		WillReturnRows(sqlmock.NewRows([]string{
			"timestamp", "event_type", "local_cluster", "remote_cluster",
			"stream_name", "stream_id", "source_node", "dest_node", "dtsc_url",
			"latency_ms", "time_to_live_ms", "failure_reason",
			"queried_clusters", "responding_clusters", "total_candidates",
			"best_remote_score", "peer_cluster", "role", "reason",
			"local_lat", "local_lon", "remote_lat", "remote_lon",
		}).AddRow(
			end, "origin_pull_completed", "cluster-a", "cluster-b",
			"stream-1", "11111111-1111-1111-1111-111111111111", "src-node", "dest-node", "https://example.com/live",
			12.5, 345.0, "",
			2, 1, 4,
			99, "cluster-b", "peer_manager", "test",
			47.6062, -122.3321, 37.7749, -122.4194,
		))

	resp, err := server.GetFederationEvents(ctx, &pb.GetFederationEventsRequest{
		TimeRange: &pb.TimeRange{Start: timestamppb.New(start), End: timestamppb.New(end)},
	})
	if err != nil {
		t.Fatalf("GetFederationEvents returned error: %v", err)
	}
	if len(resp.GetEvents()) != 1 {
		t.Fatalf("expected 1 event, got %d", len(resp.GetEvents()))
	}

	evt := resp.GetEvents()[0]
	if evt.LocalLatitude == nil || evt.RemoteLatitude == nil {
		t.Fatal("expected geo coordinates to be populated")
	}
	if *evt.LocalLatitude != 47.6062 || *evt.RemoteLongitude != -122.4194 {
		t.Fatalf("unexpected geo values: local=%v,%v remote=%v,%v", evt.GetLocalLatitude(), evt.GetLocalLongitude(), evt.GetRemoteLatitude(), evt.GetRemoteLongitude())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet mock expectations: %v", err)
	}
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

func TestSanitizeFloat32(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float32
	}{
		{name: "NaN", in: math.NaN(), want: 0},
		{name: "+Inf", in: math.Inf(1), want: 0},
		{name: "-Inf", in: math.Inf(-1), want: 0},
		{name: "zero", in: 0, want: 0},
		{name: "positive", in: 3.5, want: 3.5},
		{name: "negative", in: -2.25, want: -2.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeFloat32(tc.in); got != tc.want {
				t.Fatalf("sanitizeFloat32(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeFloat64(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{name: "NaN", in: math.NaN(), want: 0},
		{name: "+Inf", in: math.Inf(1), want: 0},
		{name: "-Inf", in: math.Inf(-1), want: 0},
		{name: "finite", in: 1.5, want: 1.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeFloat64(tc.in); got != tc.want {
				t.Fatalf("sanitizeFloat64(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizePlatformOverviewResponse(t *testing.T) {
	resp := &pb.GetPlatformOverviewResponse{
		AverageViewers:   math.NaN(),
		PeakBandwidth:    math.Inf(1),
		StreamHours:      math.Inf(-1),
		EgressGb:         42,
		ViewerHours:      math.NaN(),
		DeliveredMinutes: math.Inf(1),
		IngestHours:      7,
	}

	sanitizePlatformOverviewResponse(resp)

	if resp.AverageViewers != 0 ||
		resp.PeakBandwidth != 0 ||
		resp.StreamHours != 0 ||
		resp.ViewerHours != 0 ||
		resp.DeliveredMinutes != 0 {
		t.Fatalf("expected non-finite platform overview fields to be zeroed: %+v", resp)
	}
	if resp.EgressGb != 42 || resp.IngestHours != 7 {
		t.Fatalf("expected finite platform overview fields to be preserved: %+v", resp)
	}
}
