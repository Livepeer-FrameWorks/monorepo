//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func refreshDashboardViews(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, view := range []string{
		"periscope.tenant_usage_5m_mv",
		"periscope.tenant_usage_hourly_mv",
		"periscope.viewer_hours_hourly_mv",
		"periscope.viewer_geo_hourly_mv",
		"periscope.viewer_city_hourly_mv",
		"periscope.stream_connection_hourly_mv",
		"periscope.stream_runtime_hourly_mv",
		"periscope.processing_hourly_mv",
		"periscope.api_usage_hourly_mv",
		"periscope.tenant_viewer_daily_mv",
		"periscope.tenant_analytics_daily_mv",
		"periscope.stream_analytics_daily_mv",
		"periscope.storage_usage_hourly_mv",
	} {
		if _, err := db.ExecContext(context.Background(), "SYSTEM REFRESH VIEW "+view); err != nil {
			t.Fatalf("refresh %s: %v", view, err)
		}
		if _, err := db.ExecContext(context.Background(), "SYSTEM WAIT VIEW "+view); err != nil {
			t.Fatalf("wait %s: %v", view, err)
		}
	}
}

func TestStreamDailyAudienceUsesSessionEndDayAndExcludesEmptyGeo_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	ctx := context.Background()
	const tenantID = "7eed517e-ba5e-4a7a-817e-ba5eda7a0001"
	const streamID = "7eedfeed-11fe-4a57-8eed-11feca570001"
	day := time.Now().UTC().Add(-48 * time.Hour).Truncate(24 * time.Hour)
	started := day.Add(23*time.Hour + 58*time.Minute)
	ended := day.Add(24*time.Hour + 2*time.Minute)
	version := time.Now().UnixMilli()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO periscope.viewer_sessions_final
		(tenant_id, node_id, session_id, source_event_id, cluster_id, stream_id,
		 stream_name, host, country_code, city, duration_seconds,
		 source_started_at_ms, source_ended_at_ms, projection_version_ms, closed_reason)
		VALUES
		(?, 'node-a', 'shared-session', 'end-day-a', 'cluster-a', ?, 'daily-stream',
		 'viewer-a', 'US', 'New York', 240, ?, ?, ?, 'final'),
		(?, 'node-b', 'shared-session', 'end-day-b', 'cluster-a', ?, 'daily-stream',
		 '', '', '', 240, ?, ?, ?, 'final')
	`, tenantID, streamID, started.UnixMilli(), ended.UnixMilli(), version,
		tenantID, streamID, started.UnixMilli(), ended.UnixMilli(), version+1); err != nil {
		t.Fatal(err)
	}
	for i, window := range []time.Time{started.Truncate(5 * time.Minute), ended.Truncate(5 * time.Minute)} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO periscope.viewer_usage_5m
			(window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
			 seconds_observed, source_event_id, projection_version_ms)
			VALUES (?, ?, 'cluster-a', ?, 'node-a', 'shared-session', 120, ?, ?)
		`, window, tenantID, streamID, fmt.Sprintf("daily-window-%d", i), version+int64(i)+2); err != nil {
			t.Fatal(err)
		}
	}
	refreshDashboardViews(t, db)

	var priorViews, endViews, countries, cities uint64
	if err := db.QueryRowContext(ctx, `
		SELECT total_views FROM periscope.stream_analytics_daily
		WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)
	`, tenantID, streamID, day).Scan(&priorViews); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT total_views, unique_countries, unique_cities
		FROM periscope.stream_analytics_daily
		WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)
	`, tenantID, streamID, ended).Scan(&endViews, &countries, &cities); err != nil {
		t.Fatal(err)
	}
	if priorViews != 0 || endViews != 2 || countries != 1 || cities != 1 {
		t.Fatalf("daily session semantics prior=%d end=%d countries=%d cities=%d", priorViews, endViews, countries, cities)
	}
	var tenantPriorViews, tenantEndViews, tenantPriorSessions, tenantEndSessions uint64
	if err := db.QueryRowContext(ctx, `
		SELECT total_views FROM periscope.tenant_analytics_daily
		WHERE tenant_id = ? AND day = toDate(?)
	`, tenantID, day).Scan(&tenantPriorViews); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT total_views FROM periscope.tenant_analytics_daily
		WHERE tenant_id = ? AND day = toDate(?)
	`, tenantID, ended).Scan(&tenantEndViews); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT total_sessions FROM periscope.tenant_viewer_daily
		WHERE tenant_id = ? AND cluster_id = 'cluster-a' AND day = toDate(?)
	`, tenantID, day).Scan(&tenantPriorSessions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT total_sessions FROM periscope.tenant_viewer_daily
		WHERE tenant_id = ? AND cluster_id = 'cluster-a' AND day = toDate(?)
	`, tenantID, ended).Scan(&tenantEndSessions); err != nil {
		t.Fatal(err)
	}
	if tenantPriorViews != 0 || tenantEndViews != 2 || tenantPriorSessions != 0 || tenantEndSessions != 2 {
		t.Fatalf("tenant daily session semantics views=%d/%d sessions=%d/%d", tenantPriorViews, tenantEndViews, tenantPriorSessions, tenantEndSessions)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO periscope.restream_sessions_final
		(tenant_id, node_id, source_event_id, cluster_id, stream_id, stream_name,
		 source_generation, target_id, target_revision, mist_push_id, platform, state,
		 duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms,
		 edge_received_at_ms, projection_version_ms, payload_raw)
		VALUES (?, 'node-restream', 'cross-midnight-restream', 'cluster-a', ?, 'daily-stream',
		 toUUID('7eedfeed-11fe-4a57-8eed-11feca570002'),
		 toUUID('7eedfeed-11fe-4a57-8eed-11feca570003'), 1, 41, 'youtube', 'idle',
		 240000, ?, ?, ?, ?, ?, '{}')
	`, tenantID, streamID, uint64(2<<30), started.UnixMilli(), ended.UnixMilli(), version+10, version+10); err != nil {
		t.Fatal(err)
	}
	refreshDashboardViews(t, db)
	var tenantPriorEgress, tenantEndEgress, streamPriorEgress, streamEndEgress uint64
	if err := db.QueryRowContext(ctx, `SELECT egress_bytes FROM periscope.tenant_analytics_daily
		WHERE tenant_id = ? AND day = toDate(?)`, tenantID, day).Scan(&tenantPriorEgress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT egress_bytes FROM periscope.tenant_analytics_daily
		WHERE tenant_id = ? AND day = toDate(?)`, tenantID, ended).Scan(&tenantEndEgress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT egress_bytes FROM periscope.stream_analytics_daily
		WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)`, tenantID, streamID, day).Scan(&streamPriorEgress); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT egress_bytes FROM periscope.stream_analytics_daily
		WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)`, tenantID, streamID, ended).Scan(&streamEndEgress); err != nil {
		t.Fatal(err)
	}
	if tenantPriorEgress != 0 || streamPriorEgress != 0 || tenantEndEgress != 2<<30 || streamEndEgress != 2<<30 {
		t.Fatalf("tenant/stream egress day contracts diverged: prior=%d/%d end=%d/%d", tenantPriorEgress, streamPriorEgress, tenantEndEgress, streamEndEgress)
	}

	// Reprojection may correct the terminal timestamp across a day boundary.
	// Both the former and replacement keys must refresh or the former day keeps
	// charging the session forever.
	shiftedEnd := ended.Add(24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO periscope.restream_sessions_final
		(tenant_id, node_id, source_event_id, cluster_id, stream_id, stream_name,
		 source_generation, target_id, target_revision, mist_push_id, platform, state,
		 duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms,
		 edge_received_at_ms, projection_version_ms, payload_raw)
		VALUES (?, 'node-restream', 'cross-midnight-restream', 'cluster-a', ?, 'daily-stream',
		 toUUID('7eedfeed-11fe-4a57-8eed-11feca570002'),
		 toUUID('7eedfeed-11fe-4a57-8eed-11feca570003'), 1, 41, 'youtube', 'idle',
		 240000, ?, ?, ?, ?, ?, '{}')
	`, tenantID, streamID, uint64(2<<30), shiftedEnd.Add(-4*time.Minute).UnixMilli(), shiftedEnd.UnixMilli(), version+20, version+20); err != nil {
		t.Fatal(err)
	}
	refreshDashboardViews(t, db)
	var tenantFormer, tenantShifted, streamFormer, streamShifted uint64
	for _, check := range []struct {
		query string
		day   time.Time
		dest  *uint64
	}{
		{`SELECT egress_bytes FROM periscope.tenant_analytics_daily WHERE tenant_id = ? AND day = toDate(?)`, ended, &tenantFormer},
		{`SELECT egress_bytes FROM periscope.tenant_analytics_daily WHERE tenant_id = ? AND day = toDate(?)`, shiftedEnd, &tenantShifted},
		{`SELECT egress_bytes FROM periscope.stream_analytics_daily WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)`, ended, &streamFormer},
		{`SELECT egress_bytes FROM periscope.stream_analytics_daily WHERE tenant_id = ? AND stream_id = ? AND day = toDate(?)`, shiftedEnd, &streamShifted},
	} {
		args := []any{tenantID, check.day}
		if strings.Contains(check.query, "stream_id") {
			args = []any{tenantID, streamID, check.day}
		}
		if err := db.QueryRowContext(ctx, check.query, args...).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
	}
	if tenantFormer != 0 || streamFormer != 0 || tenantShifted != 2<<30 || streamShifted != 2<<30 {
		t.Fatalf("day-shift reprojection left stale facts: former=%d/%d shifted=%d/%d", tenantFormer, streamFormer, tenantShifted, streamShifted)
	}
}

func TestTenantDailyStatsIncludesRestreamEgressWithoutInventingViewers_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	const tenantID = "6eed517e-ba5e-4a7a-817e-ba5eda7a0001"
	const streamID = "6eedfeed-11fe-4a57-8eed-11feca570001"
	now := time.Now().UTC().Truncate(5 * time.Minute)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO periscope.delivery_usage_5m
		(window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind,
		 delivery_id, platform, seconds_observed, down_bytes_observed,
		 source_event_id, projection_version_ms)
		VALUES (?, ?, ?, ?, ?, 'restream', ?, 'youtube', 300, ?, ?, ?)
	`, now, tenantID, "cluster-restream", streamID, "node-restream",
		"restream-session-1", uint64(3<<30), "restream-event-1", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO periscope.restream_sessions_final
		(tenant_id, node_id, source_event_id, cluster_id, stream_id, stream_name,
		 source_generation, target_id, target_revision, mist_push_id, platform, state,
		 duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms,
		 edge_received_at_ms, projection_version_ms, payload_raw)
		VALUES (?, 'node-restream', 'restream-event-1', 'cluster-restream', ?, 'daily-stream',
		 toUUID('6eedfeed-11fe-4a57-8eed-11feca570002'),
		 toUUID('6eedfeed-11fe-4a57-8eed-11feca570003'), 1, 41, 'youtube', 'idle',
		 300000, ?, ?, ?, ?, ?, '{}')
	`, tenantID, streamID, uint64(3<<30), now.Add(-5*time.Minute).UnixMilli(), now.UnixMilli(), now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	server := NewPeriscopeServer(db, logging.NewLogger())
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	resp, err := server.GetTenantDailyStats(ctx, &periscopepb.GetTenantDailyStatsRequest{
		TenantId: tenantID, Days: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetStats()) != 1 {
		t.Fatalf("daily rows=%d, want 1: %+v", len(resp.GetStats()), resp.GetStats())
	}
	got := resp.GetStats()[0]
	if got.GetEgressGb() != 3 {
		t.Fatalf("restream egress=%v GiB, want 3", got.GetEgressGb())
	}
	if got.GetViewerHours() != 0 || got.GetUniqueViewers() != 0 || got.GetTotalSessions() != 0 || got.GetTotalViews() != 0 {
		t.Fatalf("restream was counted as human audience: %+v", got)
	}

	viewerWindow := now.Add(-24 * time.Hour)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO periscope.viewer_usage_5m
		(window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
		 seconds_observed, down_bytes_observed, source_event_id, projection_version_ms)
		VALUES (?, ?, ?, ?, ?, ?, 300, ?, ?, ?)
	`, viewerWindow, tenantID, "cluster-viewer", streamID, "node-viewer",
		"viewer-only-session", uint64(1<<30), "viewer-only-event", now.UnixMilli()+1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO periscope.viewer_sessions_final
		(tenant_id, node_id, session_id, source_event_id, cluster_id, stream_id,
		 stream_name, host, duration_seconds, source_started_at_ms, source_ended_at_ms,
		 projection_version_ms, closed_reason)
		VALUES (?, 'node-viewer', 'viewer-only-session', 'viewer-only-final', 'cluster-viewer', ?,
		 'daily-stream', 'viewer-only', 300, ?, ?, ?, 'final')
	`, tenantID, streamID, viewerWindow.UnixMilli(), viewerWindow.Add(5*time.Minute).UnixMilli(), now.UnixMilli()+2); err != nil {
		t.Fatal(err)
	}
	excludedWindow := now.Add(-48 * time.Hour)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO periscope.restream_sessions_final
		(tenant_id, node_id, source_event_id, cluster_id, stream_id, stream_name,
		 source_generation, target_id, target_revision, mist_push_id, platform, state,
		 duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms,
		 edge_received_at_ms, projection_version_ms, payload_raw)
		VALUES (?, 'node-restream', 'excluded-restream-event', 'cluster-restream', ?, 'daily-stream',
		 toUUID('6eedfeed-11fe-4a57-8eed-11feca570004'),
		 toUUID('6eedfeed-11fe-4a57-8eed-11feca570005'), 1, 42, 'youtube', 'idle',
		 300000, 1073741824, ?, ?, ?, ?, '{}')
	`, tenantID, streamID, excludedWindow.UnixMilli(), excludedWindow.Add(5*time.Minute).UnixMilli(), now.UnixMilli()+3, now.UnixMilli()+3); err != nil {
		t.Fatal(err)
	}
	viewerAndDelivery, err := server.GetTenantDailyStats(ctx, &periscopepb.GetTenantDailyStatsRequest{
		TenantId: tenantID, Days: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(viewerAndDelivery.GetStats()) != 2 {
		t.Fatalf("two-day range dropped an in-range day or included the third day: %+v", viewerAndDelivery.GetStats())
	}
	statsByDay := make(map[string]*periscopepb.TenantDailyStat)
	for _, stat := range viewerAndDelivery.GetStats() {
		statsByDay[stat.GetDate().AsTime().Format("2006-01-02")] = stat
	}
	viewerOnly := statsByDay[viewerWindow.Format("2006-01-02")]
	if viewerOnly == nil || viewerOnly.GetTotalViews() != 1 || viewerOnly.GetEgressGb() != 0 {
		t.Fatalf("viewer-only FULL OUTER JOIN row was malformed: %+v", viewerOnly)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO periscope.tenant_acquisition_events
		(timestamp, tenant_id, signup_channel, signup_method, is_agent, event_data)
		VALUES (?, ?, 'organic', 'email', 0, '{}')
	`, viewerWindow.Add(-time.Hour), tenantID); err != nil {
		t.Fatal(err)
	}
	timeRange := &commonpb.TimeRange{
		Start: timestamppb.New(viewerWindow.Add(-time.Hour)),
		End:   timestamppb.New(now.Add(5 * time.Minute)),
	}

	daily, err := server.GetTenantAnalyticsDaily(ctx, &periscopepb.GetTenantAnalyticsDailyRequest{
		TenantId: tenantID, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(daily.GetRecords()) != 2 {
		t.Fatalf("tenant history dropped a viewer-only or delivery-only day: %+v", daily.GetRecords())
	}
	byDay := make(map[string]*periscopepb.TenantAnalyticsDaily)
	for _, record := range daily.GetRecords() {
		byDay[record.GetDay().AsTime().Format("2006-01-02")] = record
	}
	viewerDay := byDay[viewerWindow.Format("2006-01-02")]
	deliveryDay := byDay[now.Format("2006-01-02")]
	if viewerDay == nil || viewerDay.GetTotalViews() != 1 || viewerDay.GetEgressBytes() != 0 {
		t.Fatalf("viewer-only daily history was not preserved: %+v", viewerDay)
	}
	if deliveryDay == nil || deliveryDay.GetTotalViews() != 0 || deliveryDay.GetEgressBytes() != int64(3<<30) {
		t.Fatalf("delivery-only daily history was not preserved: %+v", deliveryDay)
	}

	network, err := server.GetNetworkUsage(ctx, &periscopepb.GetNetworkUsageRequest{
		TimeRange: timeRange, GroupBy: periscopepb.NetworkUsageGroupBy_NETWORK_USAGE_GROUP_BY_DAY,
	})
	if err != nil {
		t.Fatal(err)
	}
	networkByDay := make(map[string]*periscopepb.NetworkUsageRecord)
	for _, record := range network.GetRecords() {
		networkByDay[record.GetPeriodStart().AsTime().Format("2006-01-02")] = record
	}
	if networkByDay[viewerWindow.Format("2006-01-02")] == nil || networkByDay[now.Format("2006-01-02")] == nil {
		t.Fatalf("network history dropped a one-sided usage period: %+v", network.GetRecords())
	}

	cohort, err := server.GetAcquisitionCohortUsage(ctx, &periscopepb.GetAcquisitionCohortUsageRequest{TimeRange: timeRange})
	if err != nil {
		t.Fatal(err)
	}
	cohortByDay := make(map[string]*periscopepb.AcquisitionCohortUsageRecord)
	for _, record := range cohort.GetRecords() {
		cohortByDay[record.GetDay().AsTime().Format("2006-01-02")] = record
	}
	if cohortByDay[viewerWindow.Format("2006-01-02")] == nil || cohortByDay[now.Format("2006-01-02")] == nil {
		t.Fatalf("cohort history dropped viewer-only or delivery-only usage: %+v", cohort.GetRecords())
	}

	refreshDashboardViews(t, db)
	streamDaily, err := server.GetStreamAnalyticsDaily(ctx, &periscopepb.GetStreamAnalyticsDailyRequest{
		TenantId: tenantID, StreamId: proto.String(streamID), TimeRange: timeRange,
	})
	if err != nil {
		t.Fatal(err)
	}
	var restreamDay *periscopepb.StreamAnalyticsDaily
	for _, record := range streamDaily.GetRecords() {
		if record.GetDay().AsTime().Format("2006-01-02") == now.Format("2006-01-02") {
			restreamDay = record
		}
	}
	if restreamDay == nil || restreamDay.GetEgressBytes() != int64(3<<30) || restreamDay.GetTotalViews() != 0 {
		t.Fatalf("stream daily surface did not preserve restream egress/audience separation: %+v", restreamDay)
	}

	activity, err := server.ListTenantActivity(ctx, &periscopepb.ListTenantActivityRequest{
		TenantIds: []string{tenantID}, TimeRange: timeRange,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(activity.GetTenants()) != 1 || activity.GetTenants()[0].GetEgressGb() != 3 || activity.GetTenants()[0].GetUniqueViewers() != 1 {
		t.Fatalf("operator activity did not use delivery egress with playback-only audience: %+v", activity.GetTenants())
	}
}

func TestDashboardRefreshPreservesBothPlanesAcrossOneSidedCorrections_RealClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	ctx := context.Background()
	const tenantID = "8eed517e-ba5e-4a7a-817e-ba5eda7a0001"
	const streamID = "8eedfeed-11fe-4a57-8eed-11feca570001"
	window := time.Now().UTC().Add(-5 * time.Minute).Truncate(5 * time.Minute)
	version := time.Now().UnixMilli()

	insertViewer := func(sessionID string, seconds uint32, downBytes uint64, projectionVersion int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO periscope.viewer_usage_5m
			(window_start, tenant_id, cluster_id, stream_id, node_id, session_id,
			 seconds_observed, down_bytes_observed, source_event_id, projection_version_ms)
			VALUES (?, ?, 'cluster-a', ?, 'node-a', ?, ?, ?, ?, ?)
		`, window, tenantID, streamID, sessionID, seconds, downBytes, "viewer-"+sessionID, projectionVersion); err != nil {
			t.Fatal(err)
		}
		closedReason := "final"
		if seconds == 0 {
			closedReason = "retracted"
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO periscope.viewer_sessions_final
			(tenant_id, node_id, session_id, source_event_id, cluster_id, stream_id,
			 stream_name, host, duration_seconds, downloaded_bytes, source_started_at_ms, source_ended_at_ms,
			 projection_version_ms, closed_reason)
			VALUES (?, 'node-a', ?, ?, 'cluster-a', ?, 'daily-stream', ?, ?, ?, ?, ?, ?, ?)
		`, tenantID, sessionID, "final-"+sessionID, streamID, sessionID,
			seconds, downBytes, window.UnixMilli(), window.Add(5*time.Minute).UnixMilli(), projectionVersion, closedReason); err != nil {
			t.Fatal(err)
		}
	}
	insertDelivery := func(kind, deliveryID, platform string, seconds uint32, downBytes uint64, projectionVersion int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO periscope.delivery_usage_5m
			(window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind,
			 delivery_id, platform, seconds_observed, down_bytes_observed,
			 source_event_id, projection_version_ms)
			VALUES (?, ?, 'cluster-a', ?, 'node-a', ?, ?, ?, ?, ?, ?, ?)
		`, window, tenantID, streamID, kind, deliveryID, platform, seconds, downBytes, "delivery-"+deliveryID, projectionVersion); err != nil {
			t.Fatal(err)
		}
		if kind == "restream" {
			endedAt := window.Add(time.Duration(seconds) * time.Second).UnixMilli()
			if seconds == 0 {
				endedAt = window.UnixMilli()
			}
			if _, err := db.ExecContext(ctx, `
				INSERT INTO periscope.restream_sessions_final
				(tenant_id, node_id, source_event_id, cluster_id, stream_id, stream_name,
				 source_generation, target_id, target_revision, platform, state,
				 duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms,
				 edge_received_at_ms, projection_version_ms, payload_raw)
				VALUES (?, 'node-a', ?, 'cluster-a', ?, 'daily-stream',
				 toUUID('8eedfeed-11fe-4a57-8eed-11feca570002'),
				 toUUID('8eedfeed-11fe-4a57-8eed-11feca570003'), 1, ?, 'idle',
				 ?, ?, ?, ?, ?, ?, '{}')
			`, tenantID, "final-"+deliveryID, streamID, platform,
				uint64(seconds)*1000, downBytes, window.UnixMilli(), endedAt,
				endedAt, projectionVersion); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertPlanes := func(wantSessions, wantWindowDownBytes, wantDailyDownBytes uint64) {
		t.Helper()
		var sessions, downBytes uint64
		if err := db.QueryRowContext(ctx, `
			SELECT toUInt64(finalizeAggregation(unique_sessions_state)), down_bytes
			FROM periscope.tenant_usage_5m
			WHERE tenant_id = ? AND window_start = ? AND cluster_id = 'cluster-a'
		`, tenantID, window).Scan(&sessions, &downBytes); err != nil {
			t.Fatal(err)
		}
		if sessions != wantSessions || downBytes != wantWindowDownBytes {
			t.Fatalf("5m rollup lost a plane: sessions=%d/%d down_bytes=%d/%d", sessions, wantSessions, downBytes, wantWindowDownBytes)
		}

		var dailyViews, dailyDown uint64
		if err := db.QueryRowContext(ctx, `
			SELECT total_views, egress_bytes
			FROM periscope.tenant_analytics_daily
			WHERE tenant_id = ? AND day = toDate(?)
		`, tenantID, window).Scan(&dailyViews, &dailyDown); err != nil {
			t.Fatal(err)
		}
		if dailyViews != wantSessions || dailyDown != wantDailyDownBytes {
			t.Fatalf("daily rollup lost a plane: views=%d/%d egress=%d/%d", dailyViews, wantSessions, dailyDown, wantDailyDownBytes)
		}

		var viewerSeconds uint64
		if err := db.QueryRowContext(ctx, `
			SELECT toUInt64(sum(total_session_seconds))
			FROM periscope.viewer_hours_hourly
			WHERE tenant_id = ? AND hour = toStartOfHour(?) AND stream_id = ?
		`, tenantID, window, streamID).Scan(&viewerSeconds); err != nil {
			t.Fatal(err)
		}
		if viewerSeconds != wantSessions*300 {
			t.Fatalf("viewer-hours sibling retained a tombstoned value: seconds=%d/%d", viewerSeconds, wantSessions*300)
		}
	}

	insertViewer("viewer-1", 300, 100, version)
	insertDelivery("playback", "viewer-1", "", 300, 100, version)
	insertDelivery("restream", "restream-1", "youtube", 300, 200, version)
	refreshDashboardViews(t, db)
	assertPlanes(1, 300, 300)

	// A correction from delivery alone must not replace audience aggregates
	// with default values.
	insertDelivery("restream", "restream-1", "youtube", 300, 400, version+1)
	refreshDashboardViews(t, db)
	assertPlanes(1, 500, 500)

	// Conversely, an audience-only arrival must retain delivery totals until
	// its mirrored playback ledger row arrives.
	insertViewer("viewer-2", 300, 50, version+2)
	refreshDashboardViews(t, db)
	assertPlanes(2, 500, 550)

	// Tombstones must remain visible to the affected-key selector even though
	// the finalized reader view filters their zero-valued rows out.
	insertViewer("viewer-1", 0, 0, version+3)
	insertViewer("viewer-2", 0, 0, version+3)
	refreshDashboardViews(t, db)
	assertPlanes(0, 500, 400)

	insertDelivery("playback", "viewer-1", "", 0, 0, version+4)
	insertDelivery("restream", "restream-1", "youtube", 0, 0, version+4)
	refreshDashboardViews(t, db)
	assertPlanes(0, 0, 0)

	// A late projection for a source day outside raw-ledger retention must not
	// overwrite an already-retained long-lived rollup with a partial aggregate.
	oldWindow := time.Now().UTC().Add(-120 * 24 * time.Hour).Truncate(24 * time.Hour)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO periscope.tenant_analytics_daily_store
		SELECT toDate(?), toUUID(?), toUInt64(1), toUInt64(1),
		       uniqCombinedState('retained-viewer'), toUInt64(123), now64(3)
	`, oldWindow, tenantID); err != nil {
		t.Fatal(err)
	}
	insertOldVersion := time.Now().UnixMilli() + 100
	if _, err := db.ExecContext(ctx, `
		INSERT INTO periscope.delivery_usage_5m
		(window_start, tenant_id, cluster_id, stream_id, node_id, delivery_kind,
		 delivery_id, platform, seconds_observed, down_bytes_observed,
		 source_event_id, projection_version_ms)
		VALUES (?, ?, 'cluster-old', ?, 'node-old', 'restream', 'late-old', 'custom', 300, 999, 'late-old', ?)
	`, oldWindow, tenantID, streamID, insertOldVersion); err != nil {
		t.Fatal(err)
	}
	refreshDashboardViews(t, db)
	var retainedEgress uint64
	if err := db.QueryRowContext(ctx, `
		SELECT egress_bytes FROM periscope.tenant_analytics_daily
		WHERE tenant_id = ? AND day = toDate(?)
	`, tenantID, oldWindow).Scan(&retainedEgress); err != nil {
		t.Fatal(err)
	}
	if retainedEgress != 123 {
		t.Fatalf("out-of-retention correction overwrote retained rollup: egress=%d", retainedEgress)
	}
}
