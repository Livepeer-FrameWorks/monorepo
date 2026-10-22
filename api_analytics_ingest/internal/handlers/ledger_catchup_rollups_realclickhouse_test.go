//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
)

// Rollups and billing's active-tenant scan discover ledger changes by
// projection_version_ms within a horizon of two hours to seven days. A ledger
// that fell further behind its source facts than any horizon must still write
// its catch-up rows where every one of those readers finds them.
func TestLedgerCatchupStaysVisibleToEveryLedgerReader_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-ledger-catchup-rollups").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	tenantUUID, streamUUID := uuid.MustParse(tenantID), uuid.MustParse(streamID)
	const node, cluster = "edge-catchup", "media-catchup"

	// The source facts were projected eight days ago; the ledger only now
	// catches up on them.
	projectedAt := time.Now().UTC().Add(-8 * 24 * time.Hour).Truncate(5 * time.Minute)
	versionMS := projectedAt.UnixMilli()
	startMS := projectedAt.Add(-30 * time.Minute).UnixMilli()
	endMS := projectedAt.Add(-10 * time.Minute).UnixMilli()

	seed := []struct {
		name  string
		query string
		args  []any
	}{
		{"viewer final", `INSERT INTO periscope.viewer_sessions_final
			(tenant_id, node_id, session_id, source_event_id, cluster_id, stream_id, stream_name, connector, country_code, city,
			 duration_seconds, downloaded_bytes, source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms, closed_reason)
			VALUES (?, ?, 'catchup-viewer', 'catchup-viewer-end', ?, ?, 'live+catchup', 'hls', 'NL', 'Amsterdam', 1200, 4096, ?, ?, 0, ?, 'final')`,
			[]any{tenantUUID, node, cluster, streamUUID, startMS, endMS, versionMS}},
		{"restream final", `INSERT INTO periscope.restream_sessions_final
			(tenant_id, node_id, source_event_id, cluster_id, stream_id, source_generation, target_id, target_revision,
			 platform, state, duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms)
			VALUES (?, ?, 'catchup-restream', ?, ?, ?, ?, 1, 'youtube', 'idle', ?, 8192, ?, ?, 0, ?)`,
			[]any{tenantUUID, node, cluster, streamUUID, uuid.New(), uuid.New(), uint64(endMS - startMS), startMS, endMS, versionMS}},
		{"stream final", `INSERT INTO periscope.stream_sessions_final
			(tenant_id, node_id, stream_id, source_event_id, cluster_id, stream_name, total_viewers,
			 source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms, closed_reason, ingest_session_id)
			VALUES (?, ?, ?, 'catchup-stream-end', ?, 'live+catchup', 3, ?, ?, 0, ?, 'final', ?)`,
			[]any{tenantUUID, node, streamUUID, cluster, startMS, endMS, versionMS, uuid.NewString()}},
		{"processing final", `INSERT INTO periscope.processing_segments_final
			(tenant_id, node_id, stream_id, process_type, output_codec, track_type, segment_number, source_event_id,
			 cluster_id, media_seconds, source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms)
			VALUES (?, ?, ?, 'Livepeer', 'h264', 'video', 1, 'catchup-segment', ?, 6, ?, ?, 0, ?)`,
			[]any{tenantUUID, node, streamUUID, cluster, startMS, startMS + 6000, versionMS}},
		{"api request", `INSERT INTO periscope.api_requests
			(timestamp, tenant_id, source_node, source_event_id, ingested_at_ms, auth_type, operation_name, operation_type,
			 request_count, error_count, total_duration_ms, total_complexity, user_hashes, token_hashes)
			VALUES (?, ?, 'bridge', 'catchup-api:0', ?, 'jwt', 'Streams', 'query', 1, 0, 10, 1, [1], [2])`,
			[]any{time.UnixMilli(startMS).UTC(), tenantID, versionMS}},
	}
	for _, s := range seed {
		if err := conn.Exec(ctx, s.query, s.args...); err != nil {
			t.Fatalf("seed %s: %v", s.name, err)
		}
	}

	// The scheduler runs the catch-up chunk under a lease it attempted now.
	leaseCtx := withLedgerLeaseAttempt(ctx, time.Now())
	chunkStart, chunkEnd := projectedAt.Add(-time.Minute), projectedAt.Add(time.Minute)
	for name, run := range map[string]func(context.Context, time.Time, time.Time) error{
		"viewer_usage_5m":   h.rebuildViewerUsage5m,
		"delivery_usage_5m": h.rebuildDeliveryUsage5m,
		"stream_runtime_5m": h.rebuildStreamRuntime5m,
		"processing_5m":     h.rebuildProcessing5m,
		"api_usage_5m":      h.rebuildApiUsage5m,
	} {
		if err := run(leaseCtx, chunkStart, chunkEnd); err != nil {
			t.Fatalf("catch-up %s: %v", name, err)
		}
	}
	for _, ledger := range []string{"viewer_usage_5m", "delivery_usage_5m", "stream_runtime_5m", "processing_5m", "api_usage_5m"} {
		var rows uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM periscope."+ledger+" WHERE tenant_id = ?", tenantUUID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows == 0 {
			t.Fatalf("catch-up wrote no %s rows", ledger)
		}
	}

	for _, rollup := range []struct{ view, store string }{
		{"tenant_usage_5m_mv", "tenant_usage_5m_store"},
		{"tenant_usage_hourly_mv", "tenant_usage_hourly_store"},
		{"viewer_hours_hourly_mv", "viewer_hours_hourly_store"},
		{"viewer_geo_hourly_mv", "viewer_geo_hourly_store"},
		{"viewer_city_hourly_mv", "viewer_city_hourly_store"},
		{"stream_connection_hourly_mv", "stream_connection_hourly_store"},
		{"stream_runtime_hourly_mv", "stream_runtime_hourly_store"},
		{"processing_hourly_mv", "processing_hourly_store"},
		{"api_usage_hourly_mv", "api_usage_hourly_store"},
		{"tenant_viewer_daily_mv", "tenant_viewer_daily_store"},
		{"tenant_analytics_daily_mv", "tenant_analytics_daily_store"},
		{"stream_analytics_daily_mv", "stream_analytics_daily_store"},
	} {
		if err := conn.Exec(ctx, "SYSTEM REFRESH VIEW periscope."+rollup.view); err != nil {
			t.Fatalf("refresh %s: %v", rollup.view, err)
		}
		if err := conn.Exec(ctx, "SYSTEM WAIT VIEW periscope."+rollup.view); err != nil {
			t.Fatalf("wait %s: %v", rollup.view, err)
		}
		var rows uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM periscope."+rollup.store+" WHERE tenant_id = ?", tenantUUID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows == 0 {
			t.Errorf("%s did not discover the caught-up ledger rows", rollup.view)
		}
	}

	// billing.active_tenants finds a stream-only tenant through this scan.
	var activeRuntimeRows uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.stream_runtime_5m
		WHERE tenant_id = ? AND projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)`,
		tenantUUID).Scan(&activeRuntimeRows); err != nil {
		t.Fatal(err)
	}
	if activeRuntimeRows == 0 {
		t.Errorf("billing's active-tenant scan does not see the caught-up stream_runtime_5m rows")
	}
}
