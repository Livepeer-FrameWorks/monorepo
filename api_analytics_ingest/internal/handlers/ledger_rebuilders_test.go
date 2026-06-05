package handlers

import (
	"context"
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
		"vod+demo",
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

func TestStreamRuntimeRebuilderFailsClosedWhenCustomerStreamStartIsMissing(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	end := time.Date(2026, 6, 5, 18, 58, 0, 0, time.UTC)
	conn.addQueryRow(
		"periscope.stream_sessions_final",
		uuid.NewString(),
		"edge-eu-1",
		"media-eu-1",
		uuid.NewString(),
		"vod+demo",
		"source-event-1",
		end.UnixMilli(),
		end.UnixMilli(),
		int64(7),
	)

	err := handler.rebuildStreamRuntime5m(context.Background(), end.Add(-5*time.Minute), end.Add(5*time.Minute))
	if err == nil {
		t.Fatal("expected missing customer stream start to fail closed")
	}
	if batch := conn.batches["periscope.stream_runtime_5m"]; batch != nil && len(batch.rows) > 0 {
		t.Fatalf("unexpected stream_runtime_5m rows after failed rebuild: %#v", batch.rows)
	}
}

func TestStreamRuntimeRebuilderSkipsInternalProcessingBootWithoutStart(t *testing.T) {
	conn := newFakeClickhouseConn()
	handler := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	end := time.Date(2026, 6, 5, 18, 58, 0, 0, time.UTC)
	conn.addQueryRow(
		"periscope.stream_sessions_final",
		uuid.NewString(),
		"edge-eu-1",
		"media-eu-1",
		uuid.NewString(),
		"processing+artifact",
		"source-event-1",
		end.UnixMilli(),
		end.UnixMilli(),
		int64(0),
	)

	if err := handler.rebuildStreamRuntime5m(context.Background(), end.Add(-5*time.Minute), end.Add(5*time.Minute)); err != nil {
		t.Fatalf("internal processing boot should be skipped without failing rebuild: %v", err)
	}
	if batch := conn.batches["periscope.stream_runtime_5m"]; batch != nil && len(batch.rows) > 0 {
		t.Fatalf("unexpected stream_runtime_5m rows for internal processing boot: %#v", batch.rows)
	}
}
