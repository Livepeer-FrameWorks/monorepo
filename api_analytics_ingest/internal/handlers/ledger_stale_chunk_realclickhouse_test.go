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

// The ledger lease cannot fence ClickHouse writes. A replica that lost its
// lease while stalled on chunk N can finish chunk N after another replica
// already rebuilt chunk N and N+1 from newer source state. Whichever write
// lands last, every ledger must keep the projection of the newer source
// state, including the zero rows that retract windows the newer state no
// longer covers.
func TestLedgerKeepsNewerSourceStateWhenStaleChunkLandsLast_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-ledger-stale-chunk").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	tenantUUID, streamUUID := uuid.MustParse(tenantID), uuid.MustParse(streamID)
	const node, cluster = "edge-stale", "media-stale"

	chunkN := time.Now().UTC().Add(-30 * time.Minute).Truncate(5 * time.Minute)
	chunkNext := chunkN.Add(5 * time.Minute)
	chunkEnd := chunkNext.Add(5 * time.Minute)
	olderVersionMS := chunkN.UnixMilli() + 1000
	newerVersionMS := chunkNext.UnixMilli() + 1000

	sessionStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(5 * time.Minute)
	startMS := sessionStart.UnixMilli()
	longEndMS := sessionStart.Add(15 * time.Minute).UnixMilli()
	shortEndMS := sessionStart.Add(5 * time.Minute).UnixMilli()

	// Viewer session: the newer final shortens it from three windows to one,
	// so its rebuild retracts the two windows it no longer covers.
	const viewerSession = "stale-chunk-viewer"
	for _, fact := range []struct {
		versionMS, endMS int64
		down             uint64
	}{{olderVersionMS, longEndMS, 900}, {newerVersionMS, shortEndMS, 300}} {
		if err := conn.Exec(ctx, `INSERT INTO periscope.viewer_sessions_final
			(tenant_id, node_id, session_id, source_event_id, cluster_id, stream_id, duration_seconds,
			 downloaded_bytes, source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms, closed_reason)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, 'final')`,
			tenantUUID, node, viewerSession, "viewer-end", cluster, streamUUID, uint32((fact.endMS-startMS)/1000),
			fact.down, startMS, fact.endMS, fact.versionMS); err != nil {
			t.Fatalf("seed viewer final: %v", err)
		}
	}

	// Restream session: the newer final shortens it the same way.
	const restreamSource = "stale-chunk-restream"
	for _, fact := range []struct {
		versionMS, endMS int64
		bytes            uint64
	}{{olderVersionMS, longEndMS, 9000}, {newerVersionMS, shortEndMS, 3000}} {
		if err := conn.Exec(ctx, `INSERT INTO periscope.restream_sessions_final
			(tenant_id, node_id, source_event_id, cluster_id, stream_id, source_generation, target_id, target_revision,
			 platform, state, duration_ms, bytes_sent, source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'youtube', 'idle', ?, ?, ?, ?, 0, ?)`,
			tenantUUID, node, restreamSource, cluster, streamUUID, uuid.New(), uuid.New(),
			uint64(fact.endMS-startMS), fact.bytes, startMS, fact.endMS, fact.versionMS); err != nil {
			t.Fatalf("seed restream final: %v", err)
		}
	}

	// Stream session: the newer final corrects its peak viewers.
	const streamSource = "stale-chunk-stream-end"
	ingestSession := uuid.NewString()
	for _, fact := range []struct {
		versionMS int64
		viewers   int64
	}{{olderVersionMS, 2}, {newerVersionMS, 7}} {
		if err := conn.Exec(ctx, `INSERT INTO periscope.stream_sessions_final
			(tenant_id, node_id, stream_id, source_event_id, cluster_id, stream_name, total_viewers,
			 source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms, closed_reason, ingest_session_id)
			VALUES (?, ?, ?, ?, ?, 'live+stale-chunk', ?, ?, ?, 0, ?, 'final', ?)`,
			tenantUUID, node, streamUUID, streamSource, cluster, fact.viewers, startMS, longEndMS, fact.versionMS, ingestSession); err != nil {
			t.Fatalf("seed stream final: %v", err)
		}
	}

	// Processing segment: the newer final corrects its media seconds.
	const segmentSource = "stale-chunk-segment"
	for _, fact := range []struct {
		versionMS    int64
		mediaSeconds float64
	}{{olderVersionMS, 4}, {newerVersionMS, 6}} {
		if err := conn.Exec(ctx, `INSERT INTO periscope.processing_segments_final
			(tenant_id, node_id, stream_id, process_type, output_codec, track_type, segment_number, source_event_id,
			 cluster_id, media_seconds, source_started_at_ms, source_ended_at_ms, edge_received_at_ms, projection_version_ms)
			VALUES (?, ?, ?, 'Livepeer', 'h264', 'video', 1, ?, ?, ?, ?, ?, 0, ?)`,
			tenantUUID, node, streamUUID, segmentSource, cluster, fact.mediaSeconds, startMS, startMS+6000, fact.versionMS); err != nil {
			t.Fatalf("seed processing final: %v", err)
		}
	}

	rebuilders := []struct {
		name string
		run  func(context.Context, time.Time, time.Time) error
	}{
		{"viewer_usage_5m", h.rebuildViewerUsage5m},
		{"delivery_usage_5m", h.rebuildDeliveryUsage5m},
		{"stream_runtime_5m", h.rebuildStreamRuntime5m},
		{"processing_5m", h.rebuildProcessing5m},
	}
	rebuild := func(who string, start, end time.Time) {
		t.Helper()
		for _, r := range rebuilders {
			if err := r.run(ctx, start, end); err != nil {
				t.Fatalf("%s %s [%s, %s): %v", who, r.name, start, end, err)
			}
		}
	}
	requireNewerState := func(phase string) {
		t.Helper()
		var viewerSeconds, viewerWindows, viewerDown uint64
		if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed), count(), sum(down_bytes_observed)
			FROM periscope.viewer_usage_5m_v WHERE tenant_id = ? AND node_id = ? AND session_id = ?`,
			tenantUUID, node, viewerSession).Scan(&viewerSeconds, &viewerWindows, &viewerDown); err != nil {
			t.Fatal(err)
		}
		if viewerSeconds != 300 || viewerWindows != 1 || viewerDown != 300 {
			t.Errorf("%s: viewer_usage_5m_v seconds=%d windows=%d down=%d, want the newer final's 300/1/300", phase, viewerSeconds, viewerWindows, viewerDown)
		}

		var restreamSeconds, restreamWindows, restreamBytes uint64
		if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed), count(), sum(down_bytes_observed)
			FROM periscope.delivery_usage_5m_v
			WHERE tenant_id = ? AND node_id = ? AND delivery_kind = 'restream' AND delivery_id = ?`,
			tenantUUID, node, restreamSource).Scan(&restreamSeconds, &restreamWindows, &restreamBytes); err != nil {
			t.Fatal(err)
		}
		if restreamSeconds != 300 || restreamWindows != 1 || restreamBytes != 3000 {
			t.Errorf("%s: delivery_usage_5m_v restream seconds=%d windows=%d bytes=%d, want the newer final's 300/1/3000", phase, restreamSeconds, restreamWindows, restreamBytes)
		}

		var playbackSeconds, playbackWindows uint64
		if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed), count()
			FROM periscope.delivery_usage_5m_v
			WHERE tenant_id = ? AND node_id = ? AND delivery_kind = 'playback' AND delivery_id = ?`,
			tenantUUID, node, viewerSession).Scan(&playbackSeconds, &playbackWindows); err != nil {
			t.Fatal(err)
		}
		if playbackSeconds != 300 || playbackWindows != 1 {
			t.Errorf("%s: delivery_usage_5m_v playback seconds=%d windows=%d, want the newer final's 300/1", phase, playbackSeconds, playbackWindows)
		}

		var runtimeSeconds uint64
		var maxPeak uint32
		if err := conn.QueryRow(ctx, `SELECT sum(active_seconds), max(peak_viewers)
			FROM periscope.stream_runtime_5m_v WHERE tenant_id = ? AND stream_id = ?`,
			tenantUUID, streamUUID).Scan(&runtimeSeconds, &maxPeak); err != nil {
			t.Fatal(err)
		}
		if runtimeSeconds != 900 || maxPeak != 7 {
			t.Errorf("%s: stream_runtime_5m_v runtime=%ds peak=%d, want 900s at the newer final's peak 7", phase, runtimeSeconds, maxPeak)
		}

		var mediaSeconds float64
		if err := conn.QueryRow(ctx, `SELECT sum(media_seconds)
			FROM periscope.processing_5m_v WHERE tenant_id = ? AND source_event_id = ?`,
			tenantUUID, segmentSource).Scan(&mediaSeconds); err != nil {
			t.Fatal(err)
		}
		if mediaSeconds != 6 {
			t.Errorf("%s: processing_5m_v media_seconds=%v, want the newer final's 6", phase, mediaSeconds)
		}
	}

	// Replica B rebuilds chunk N and then chunk N+1; the stalled replica A
	// finishes its chunk N projection afterwards.
	rebuild("B", chunkN, chunkNext)
	rebuild("B", chunkNext, chunkEnd)
	requireNewerState("after B rebuilt chunk N+1")
	rebuild("stale A", chunkN, chunkNext)
	requireNewerState("after stale A rebuilt chunk N")
}

// API usage rows aggregate every source event of their window, so a stale
// pass over an older ingest chunk recomputes from all events stored by then.
// Its rows must not displace those of the pass that saw the newest event.
func TestAPIUsageLedgerKeepsNewestSourceStateWhenStaleChunkLandsLast_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-api-ledger-stale-chunk").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID := uuid.NewString()
	requestAt := time.Now().UTC().Add(-time.Hour).Truncate(5 * time.Minute).Add(time.Minute)
	chunkN := time.Now().UTC().Add(-30 * time.Minute).Truncate(5 * time.Minute)
	chunkNext := chunkN.Add(5 * time.Minute)
	chunkEnd := chunkNext.Add(5 * time.Minute)

	insert := func(sourceEventID string, ingestedAt time.Time) {
		t.Helper()
		if err := conn.Exec(ctx, `INSERT INTO periscope.api_requests
			(timestamp, tenant_id, source_node, source_event_id, ingested_at_ms, auth_type, operation_name, operation_type,
			 request_count, error_count, total_duration_ms, total_complexity, user_hashes, token_hashes)
			VALUES (?, ?, 'bridge', ?, ?, 'jwt', 'Streams', 'query', 1, 0, 10, 1, [1], [2])`,
			requestAt, tenantID, sourceEventID, ingestedAt.UnixMilli()); err != nil {
			t.Fatalf("seed api request: %v", err)
		}
	}
	insert("stale-chunk-api:0", chunkN.Add(time.Second))
	insert("stale-chunk-api:1", chunkNext.Add(time.Second))

	for _, chunk := range [][2]time.Time{{chunkN, chunkNext}, {chunkNext, chunkEnd}, {chunkN, chunkNext}} {
		if err := h.rebuildApiUsage5m(ctx, chunk[0], chunk[1]); err != nil {
			t.Fatalf("api_usage_5m [%s, %s): %v", chunk[0], chunk[1], err)
		}
	}
	var requests uint64
	var latestVersionMS int64
	if err := conn.QueryRow(ctx, `SELECT sum(requests), max(latest_projection_version_ms)
		FROM periscope.api_usage_5m_v WHERE tenant_id = ?`, tenantID).Scan(&requests, &latestVersionMS); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || latestVersionMS != chunkNext.Add(time.Second).UnixMilli() {
		t.Fatalf("api_usage_5m_v requests=%d version=%d, want 2 at the newest event's ingest %d",
			requests, latestVersionMS, chunkNext.Add(time.Second).UnixMilli())
	}
}
