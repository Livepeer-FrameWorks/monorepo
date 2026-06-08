package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

// windowsForSpan is the bucketing primitive every ledger rebuilder uses
// to split a source-time span across 5-minute windows. These fixtures
// cover the cases the contract calls out: boundary-crossing spans,
// degenerate spans, and zero-length cases.
func TestWindowsForSpan_SplitsAcrossBoundary(t *testing.T) {
	// 7-minute span starting at 12:03:00 and ending at 12:10:00.
	// 12:03 → 12:05  =  2 min in window starting 12:00
	// 12:05 → 12:10  =  5 min in window starting 12:05
	start := time.Date(2026, 5, 23, 12, 3, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 5, 23, 12, 10, 0, 0, time.UTC).UnixMilli()
	got := windowsForSpan(start, end)

	if len(got) != 2 {
		t.Fatalf("expected 2 windows for a span crossing one 5-min boundary, got %d: %v", len(got), got)
	}
	w12_00 := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC).UnixMilli()
	w12_05 := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC).UnixMilli()
	if got[w12_00] != 2*60*1000 {
		t.Fatalf("expected 120000 ms in 12:00 window, got %d", got[w12_00])
	}
	if got[w12_05] != 5*60*1000 {
		t.Fatalf("expected 300000 ms in 12:05 window, got %d", got[w12_05])
	}
	// Sum across windows must equal total span — meter-contracts invariant.
	if got[w12_00]+got[w12_05] != end-start {
		t.Fatalf("overlap sum %d != span %d", got[w12_00]+got[w12_05], end-start)
	}
}

func TestWindowsForSpan_SpansManyWindows(t *testing.T) {
	// A 24-hour span starting at 00:00:00 should produce 24*12 = 288
	// windows, each 5 minutes long.
	start := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC).UnixMilli()
	end := start + 24*60*60*1000
	got := windowsForSpan(start, end)

	expected := 24 * 12
	if len(got) != expected {
		t.Fatalf("expected %d windows for a 24-hour span, got %d", expected, len(got))
	}
	for w, ms := range got {
		if ms != 5*60*1000 {
			t.Fatalf("window %d has overlap %d ms, expected 300000", w, ms)
		}
	}
}

func TestWindowsForSpan_DegenerateAndZero(t *testing.T) {
	start := time.Date(2026, 5, 23, 12, 3, 0, 0, time.UTC).UnixMilli()

	// Zero-length span — no windows.
	if got := windowsForSpan(start, start); len(got) != 0 {
		t.Fatalf("zero-length span should produce no windows, got %v", got)
	}

	// Negative span — no windows.
	if got := windowsForSpan(start, start-1); len(got) != 0 {
		t.Fatalf("negative span should produce no windows, got %v", got)
	}

	// Span that fits entirely inside one window.
	end := start + 30*1000 // 30 seconds
	got := windowsForSpan(start, end)
	if len(got) != 1 {
		t.Fatalf("sub-window span should produce one window, got %d", len(got))
	}
	for _, ms := range got {
		if ms != 30*1000 {
			t.Fatalf("expected 30000 ms overlap, got %d", ms)
		}
	}
}

func TestWindowsForSpan_AlignedBoundary(t *testing.T) {
	// Span starting exactly on a 5-min boundary and ending exactly on
	// the next — single window, full 5 minutes.
	start := time.Date(2026, 5, 23, 12, 5, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 5, 23, 12, 10, 0, 0, time.UTC).UnixMilli()
	got := windowsForSpan(start, end)

	if len(got) != 1 {
		t.Fatalf("boundary-aligned 5-min span should produce 1 window, got %d", len(got))
	}
	if got[start] != 5*60*1000 {
		t.Fatalf("expected full 5-min overlap in the start window, got %d", got[start])
	}
	// The 12:10 window itself must not appear (half-open).
	if _, ok := got[end]; ok {
		t.Fatalf("end boundary should be exclusive, got an entry at %d", end)
	}
}

func TestViewerUsageTombstonesOnlyRetractsStaleNonZeroWindows(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	currentWindow := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC).UnixMilli()
	staleWindow := time.Date(2026, 6, 3, 12, 5, 0, 0, time.UTC).UnixMilli()
	alreadyZeroWindow := time.Date(2026, 6, 3, 12, 10, 0, 0, time.UTC).UnixMilli()
	conn.addQueryRow("periscope.viewer_usage_5m", currentWindow, "cluster-a", "stream-a", "source-current", uint32(60), uint64(100), uint64(200))
	conn.addQueryRow("periscope.viewer_usage_5m", staleWindow, "cluster-a", "stream-a", "source-stale", uint32(60), uint64(100), uint64(200))
	conn.addQueryRow("periscope.viewer_usage_5m", alreadyZeroWindow, "cluster-a", "stream-a", "source-zero", uint32(0), uint64(0), uint64(0))

	tombstones, err := handler.viewerUsageTombstones(context.Background(), "tenant-a", "node-a", "session-a", map[viewerUsageWindowKey]struct{}{
		{windowStartMS: currentWindow, clusterID: "cluster-a", streamID: "stream-a"}: {},
	})
	if err != nil {
		t.Fatalf("viewerUsageTombstones: %v", err)
	}
	if len(tombstones) != 1 {
		t.Fatalf("expected one tombstone, got %#v", tombstones)
	}
	got := tombstones[0]
	if got.windowStartMS != staleWindow || got.clusterID != "cluster-a" || got.streamID != "stream-a" || got.sourceEventID != "source-stale" {
		t.Fatalf("unexpected tombstone: %#v", got)
	}
}

// TestRebuildViewerUsage5mSplitsSessionAcrossWindows verifies the forward
// projection: a finalized viewer session is split across every 5-minute window
// it overlaps, with observed seconds and up/down bytes attributed in proportion
// to each window's overlap, carrying the session's source_event_id for dedup.
func TestRebuildViewerUsage5mSplitsSessionAcrossWindows(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	start := time.Date(2026, 6, 3, 12, 3, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2026, 6, 3, 12, 10, 0, 0, time.UTC).UnixMilli()
	const upBytes, downBytes = uint64(7000), uint64(14000)

	// viewer_sessions_final scan order: tenant, node, session, source_event_id,
	// cluster, stream, started_ms, ended_ms, duration_seconds, up, down.
	conn.addQueryRow("periscope.viewer_sessions_final",
		"t1", "n1", "s1", "evt-1", "c1", "stream-1",
		start, end, uint32(420), upBytes, downBytes)

	if err := handler.rebuildViewerUsage5m(context.Background(), time.UnixMilli(start), time.UnixMilli(end)); err != nil {
		t.Fatalf("rebuildViewerUsage5m: %v", err)
	}

	batch := conn.batches["periscope.viewer_usage_5m"]
	if batch == nil || len(batch.rows) != 2 {
		t.Fatalf("expected 2 viewer_usage_5m rows (one per overlapped window), got %#v", batch)
	}

	w1200 := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC).UnixMilli()
	w1205 := time.Date(2026, 6, 3, 12, 5, 0, 0, time.UTC).UnixMilli()
	byWindow := map[int64][]any{}
	for _, row := range batch.rows {
		byWindow[row[0].(time.Time).UnixMilli()] = row
	}

	totalSpan := float64(end - start)
	check := func(windowMS, overlapMS int64) {
		row, ok := byWindow[windowMS]
		if !ok {
			t.Fatalf("missing row for window %d", windowMS)
		}
		if row[1] != "t1" || row[2] != "c1" || row[3] != "stream-1" || row[9] != "evt-1" {
			t.Fatalf("identity/source_event mismatch in window %d: %#v", windowMS, row)
		}
		if got := row[6].(uint32); got != uint32(overlapMS/1000) {
			t.Fatalf("window %d seconds_observed=%d, want %d", windowMS, got, overlapMS/1000)
		}
		fraction := float64(overlapMS) / totalSpan
		wantUp := uint64(float64(upBytes) * fraction)
		wantDown := uint64(float64(downBytes) * fraction)
		if row[7].(uint64) != wantUp || row[8].(uint64) != wantDown {
			t.Fatalf("window %d bytes=(%v,%v), want (%d,%d)", windowMS, row[7], row[8], wantUp, wantDown)
		}
	}
	check(w1200, 120000) // 12:03 → 12:05 = 2 min in the 12:00 window
	check(w1205, 300000) // 12:05 → 12:10 = 5 min in the 12:05 window
}

// TestRebuildStorageGBSeconds5mIntegratesClosedWindow verifies the GB-seconds
// integration: bytes held constant over an interval are integrated into the
// 5-minute windows the interval spans, and only a window proven closed (a later
// snapshot at-or-after its end) is emitted.
func TestRebuildStorageGBSeconds5mIntegratesClosedWindow(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	w := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	const oneGiB = uint64(1024 * 1024 * 1024)

	// storage_snapshots scan order: tenant, cluster, scope, provider_tenant,
	// provider_cluster, backend, ts_ms, ingested_at_ms, total_bytes, file_count.
	// Snapshot at the window start holds 1 GiB; the next at the window end both
	// integrates the held value across the full 5 minutes and closes the window.
	conn.addQueryRow("periscope.storage_snapshots",
		"t1", "c1", "origin", "t1", "c1", "s3",
		w.UnixMilli(), w.UnixMilli(), oneGiB, uint32(4))
	conn.addQueryRow("periscope.storage_snapshots",
		"t1", "c1", "origin", "t1", "c1", "s3",
		w.Add(5*time.Minute).UnixMilli(), w.Add(5*time.Minute).UnixMilli(), 2*oneGiB, uint32(5))

	if err := handler.rebuildStorageGBSeconds5m(context.Background(), w, w.Add(5*time.Minute)); err != nil {
		t.Fatalf("rebuildStorageGBSeconds5m: %v", err)
	}

	batch := conn.batches["periscope.storage_gb_seconds_5m"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected exactly one closed window emitted, got %#v", batch)
	}
	row := batch.rows[0]
	if row[0].(time.Time).UnixMilli() != w.UnixMilli() {
		t.Fatalf("window_start=%v, want %v", row[0], w)
	}
	// 1 GiB held for 300 s = 300 GB-seconds.
	if gbs := row[7].(float64); gbs != 300.0 {
		t.Fatalf("gb_seconds=%v, want 300", gbs)
	}
	// file_count is the value in force during the integrated interval (4).
	if fc := row[8].(uint64); fc != 4 {
		t.Fatalf("file_count=%d, want 4", fc)
	}
}

// TestStorageProjectionDiverged covers the three branches of the divergence
// guard: no prior projection (no-op), an equal prior (no-op), and a differing
// prior (records a projection_divergences row).
func TestStorageProjectionDiverged(t *testing.T) {
	const (
		tenant, cluster, scope = "t1", "c1", "origin"
		pTenant, pCluster      = "t1", "c1"
		backend                = "s3"
	)
	w := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	call := func(conn *fakeClickhouseConn, gbSeconds float64, fileCount uint64) error {
		h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
		return h.storageProjectionDiverged(context.Background(), w.UnixMilli(),
			w, tenant, cluster, scope, pTenant, pCluster, backend, gbSeconds, fileCount)
	}

	t.Run("no prior is noop", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		if err := call(conn, 300.0, 4); err != nil {
			t.Fatalf("storageProjectionDiverged: %v", err)
		}
		if conn.batches["periscope.projection_divergences"] != nil {
			t.Fatal("no prior projection must not record a divergence")
		}
	})

	t.Run("equal prior is noop", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		// divergence lookup scans (gb_seconds float64, file_count uint64).
		conn.addQueryRow("periscope.storage_gb_seconds_5m", float64(300.0), uint64(4))
		if err := call(conn, 300.0, 4); err != nil {
			t.Fatalf("storageProjectionDiverged: %v", err)
		}
		if conn.batches["periscope.projection_divergences"] != nil {
			t.Fatal("an unchanged prior must not record a divergence")
		}
	})

	t.Run("differing prior records divergence", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		conn.addQueryRow("periscope.storage_gb_seconds_5m", float64(250.0), uint64(4))
		if err := call(conn, 300.0, 4); err != nil {
			t.Fatalf("storageProjectionDiverged: %v", err)
		}
		batch := conn.batches["periscope.projection_divergences"]
		if batch == nil || len(batch.rows) != 1 {
			t.Fatalf("differing prior must record one divergence, got %#v", batch)
		}
		if batch.rows[0][1] != "storage_gb_seconds_5m" {
			t.Fatalf("divergence table_name=%v, want storage_gb_seconds_5m", batch.rows[0][1])
		}
	})
}

// TestRebuildProcessing5mPassesThroughAggregatedRow verifies the SELECT-to-
// INSERT column correspondence: the SQL does the grouping/sum, and the Go code
// must scan and re-append the identity columns and media_seconds in the same
// order. A drift between scan and append order would misfile billing identity.
func TestRebuildProcessing5mPassesThroughAggregatedRow(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	w := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	// processing_segments_final result scan order: window_start, tenant,
	// cluster, stream, process_type, output_codec, track_type, source_event_id,
	// media_seconds.
	conn.addQueryRow("periscope.processing_segments_final",
		w, "t1", "c1", "stream-1", "transcode", "h264", "video", "evt-1", float64(42.5))

	if err := handler.rebuildProcessing5m(context.Background(), w, w.Add(5*time.Minute)); err != nil {
		t.Fatalf("rebuildProcessing5m: %v", err)
	}

	batch := conn.batches["periscope.processing_5m"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected one processing_5m row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[1] != "t1" || row[2] != "c1" || row[3] != "stream-1" {
		t.Fatalf("identity columns mismatch: %#v", row)
	}
	if row[4] != "transcode" || row[5] != "h264" || row[6] != "video" || row[7] != "evt-1" {
		t.Fatalf("classification columns mismatch: %#v", row)
	}
	if row[8].(float64) != 42.5 {
		t.Fatalf("media_seconds=%v, want 42.5", row[8])
	}
}

// TestRebuildApiUsage5mInsertTargetsRollup pins the INSERT...SELECT contract.
// The aggregation is delegated to ClickHouse (uniqCombined states cannot be
// built in Go), so the unit test asserts the statement is issued against the
// expected source/target with the rollup grouping rather than the resulting
// rows (the fake cannot execute the SELECT).
func TestRebuildApiUsage5mInsertTargetsRollup(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	ws := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	if err := handler.rebuildApiUsage5m(context.Background(), ws, ws.Add(5*time.Minute)); err != nil {
		t.Fatalf("rebuildApiUsage5m: %v", err)
	}
	if len(conn.execs) != 1 {
		t.Fatalf("expected one INSERT...SELECT exec, got %d", len(conn.execs))
	}
	q := conn.execs[0].query
	for _, want := range []string{
		"INSERT INTO periscope.api_usage_5m",
		"FROM periscope.api_requests",
		"GROUP BY window_start, tenant_id, auth_type, operation_type, operation_name",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("api_usage_5m insert missing %q in:\n%s", want, q)
		}
	}
}

// TestLedgerRebuildCursor covers the cursor read/write that gates each
// rebuilder's replay span: a missing or non-positive cursor falls back to the
// default start; a stored positive cursor is returned; the writer records the
// processed-through watermark under the ledger name.
func TestLedgerRebuildCursor(t *testing.T) {
	defaultStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	t.Run("missing cursor returns default", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
		got, err := h.getLedgerRebuildCursor(context.Background(), "viewer_usage_5m", defaultStart)
		if err != nil {
			t.Fatalf("getLedgerRebuildCursor: %v", err)
		}
		if !got.Equal(defaultStart) {
			t.Fatalf("missing cursor = %v, want default %v", got, defaultStart)
		}
	})

	t.Run("non-positive cursor returns default", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		conn.addQueryRow("periscope.ledger_rebuild_cursors", int64(0))
		h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
		got, err := h.getLedgerRebuildCursor(context.Background(), "viewer_usage_5m", defaultStart)
		if err != nil {
			t.Fatalf("getLedgerRebuildCursor: %v", err)
		}
		if !got.Equal(defaultStart) {
			t.Fatalf("zero cursor = %v, want default %v", got, defaultStart)
		}
	})

	t.Run("stored cursor is returned", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		stored := time.Date(2026, 6, 3, 12, 5, 0, 0, time.UTC)
		conn.addQueryRow("periscope.ledger_rebuild_cursors", stored.UnixMilli())
		h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
		got, err := h.getLedgerRebuildCursor(context.Background(), "viewer_usage_5m", defaultStart)
		if err != nil {
			t.Fatalf("getLedgerRebuildCursor: %v", err)
		}
		if !got.Equal(stored) {
			t.Fatalf("stored cursor = %v, want %v", got, stored)
		}
	})

	t.Run("record writes watermark", func(t *testing.T) {
		conn := newFakeClickhouseConn()
		h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
		processed := time.Date(2026, 6, 3, 12, 5, 0, 0, time.UTC)
		if err := h.recordLedgerRebuildCursor(context.Background(), "viewer_usage_5m", processed); err != nil {
			t.Fatalf("recordLedgerRebuildCursor: %v", err)
		}
		batch := conn.batches["periscope.ledger_rebuild_cursors"]
		if batch == nil || len(batch.rows) != 1 {
			t.Fatalf("expected one cursor row, got %#v", batch)
		}
		if batch.rows[0][0] != "viewer_usage_5m" || batch.rows[0][1].(int64) != processed.UnixMilli() {
			t.Fatalf("cursor row = %#v, want [viewer_usage_5m %d ...]", batch.rows[0], processed.UnixMilli())
		}
	})
}

func TestStreamRuntimeRebuilderResolvesZeroDurationFinalFromEventLog(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	start := time.Date(2026, 6, 5, 18, 56, 0, 0, time.UTC)
	end := start.Add(2 * time.Minute)

	conn.addQueryRow(
		"periscope.stream_sessions_final",
		tenantID,
		"edge-eu-1",
		"media-eu-1",
		streamID,
		"live+demo",
		"source-event-1",
		end.UnixMilli(),
		end.UnixMilli(),
		int64(7),
	)
	conn.addQueryRow("periscope.stream_event_log", start.UnixMilli())

	if err := handler.rebuildStreamRuntime5m(context.Background(), end.Add(-5*time.Minute), end.Add(5*time.Minute)); err != nil {
		t.Fatalf("rebuildStreamRuntime5m: %v", err)
	}

	batch := conn.batches["periscope.stream_runtime_5m"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected one stream_runtime_5m row, got %#v", batch)
	}
	row := batch.rows[0]
	if row[2] != "media-eu-1" || row[3] != streamID {
		t.Fatalf("unexpected stream runtime identity row: %#v", row)
	}
	if got := row[4]; got != uint32(120) {
		t.Fatalf("active_seconds = %#v, want 120", got)
	}
}

func TestStreamRuntimeRebuilderSkipsMissingStartAndContinues(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	end := time.Date(2026, 6, 5, 18, 58, 0, 0, time.UTC)
	validStart := end.Add(-2 * time.Minute)
	validStreamID := uuid.NewString()
	conn.addQueryRow(
		"periscope.stream_sessions_final",
		uuid.NewString(),
		"edge-eu-1",
		"media-eu-1",
		uuid.NewString(),
		"live+demo",
		"source-event-1",
		end.UnixMilli(),
		end.UnixMilli(),
		int64(7),
	)
	conn.addQueryRow(
		"periscope.stream_sessions_final",
		uuid.NewString(),
		"edge-eu-1",
		"media-eu-1",
		validStreamID,
		"live+valid",
		"source-event-valid",
		validStart.UnixMilli(),
		end.UnixMilli(),
		int64(3),
	)

	if err := handler.rebuildStreamRuntime5m(context.Background(), end.Add(-5*time.Minute), end.Add(5*time.Minute)); err != nil {
		t.Fatalf("rebuildStreamRuntime5m: %v", err)
	}
	batch := conn.batches["periscope.stream_runtime_5m"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected one stream_runtime_5m row for valid stream, got %#v", batch)
	}
	row := batch.rows[0]
	if row[3] != validStreamID {
		t.Fatalf("expected valid stream row, got %#v", row)
	}
}

func TestStreamRuntimeRebuilderEmitsLiveStateWindow(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	windowStart := time.Date(2026, 6, 5, 19, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(5 * time.Minute)
	startedAt := windowStart.Add(-2 * time.Minute)

	conn.addQueryRow(
		"periscope.stream_state_current",
		tenantID,
		"edge-eu-1",
		"media-eu-1",
		streamID,
		"live+demo",
		startedAt,
		int64(5),
	)

	if err := handler.rebuildStreamRuntime5m(context.Background(), windowStart, windowEnd); err != nil {
		t.Fatalf("rebuildStreamRuntime5m: %v", err)
	}

	var liveSourceQuery string
	for _, q := range conn.queries {
		if q.table == "periscope.stream_state_current" {
			liveSourceQuery = q.query
			break
		}
	}
	if !strings.Contains(liveSourceQuery, "FROM periscope.stream_state_current AS s FINAL") {
		t.Fatalf("live source query must alias stream_state_current before FINAL; got:\n%s", liveSourceQuery)
	}
	if strings.Contains(liveSourceQuery, "stream_state_current FINAL AS") {
		t.Fatalf("live source query uses invalid ClickHouse FINAL alias order:\n%s", liveSourceQuery)
	}

	batch := conn.batches["periscope.stream_runtime_5m"]
	if batch == nil || len(batch.rows) != 1 {
		t.Fatalf("expected one stream_runtime_5m row for live state, got %#v", batch)
	}
	row := batch.rows[0]
	if row[2] != "media-eu-1" || row[3] != streamID {
		t.Fatalf("unexpected stream runtime identity row: %#v", row)
	}
	if got := row[4]; got != uint32(300) {
		t.Fatalf("active_seconds = %#v, want 300", got)
	}
	wantKey := streamRuntimeSessionKey(tenantID, "edge-eu-1", streamID, startedAt.UnixMilli())
	if row[6] != wantKey {
		t.Fatalf("source_event_id = %#v, want %q", row[6], wantKey)
	}
}

func TestStreamRuntimeRebuilderSkipsArtifactRuntimeWithoutStart(t *testing.T) {
	for _, streamName := range []string{"vod+artifact", "dvr+artifact", "processing+artifact"} {
		t.Run(streamName, func(t *testing.T) {
			conn := newFakeClickhouseConn()
			handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

			end := time.Date(2026, 6, 5, 18, 58, 0, 0, time.UTC)
			conn.addQueryRow(
				"periscope.stream_sessions_final",
				uuid.NewString(),
				"edge-eu-1",
				"media-eu-1",
				uuid.NewString(),
				streamName,
				"source-event-1",
				end.UnixMilli(),
				end.UnixMilli(),
				int64(0),
			)

			if err := handler.rebuildStreamRuntime5m(context.Background(), end.Add(-5*time.Minute), end.Add(5*time.Minute)); err != nil {
				t.Fatalf("artifact runtime should be skipped without failing rebuild: %v", err)
			}
			if batch := conn.batches["periscope.stream_runtime_5m"]; batch != nil && len(batch.rows) > 0 {
				t.Fatalf("unexpected stream_runtime_5m rows for artifact runtime: %#v", batch.rows)
			}
		})
	}
}
