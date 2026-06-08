package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestStaleCloseViewerSessionsEmitsAnomalyRow proves the core invariant: a stale
// live viewer session (returned by the scan) is materialized into one
// viewer_sessions_anomalous row that preserves its natural key, carries the
// observed duration/window, and is stamped "stale" with a fresh
// projection_version_ms so repeated passes dedupe via argMax.
func TestStaleCloseViewerSessionsEmitsAnomalyRow(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	// Scan columns: tenant, stream, session, node, observedFirstMs, observedLastMs, duration.
	conn.addQueryRow("periscope.viewer_sessions_current",
		"tenant-1", "stream-1", "session-1", "node-1",
		int64(1_700_000_000_000), int64(1_700_000_300_000), uint32(300))

	if err := h.staleCloseViewerSessions(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := conn.batches["periscope.viewer_sessions_anomalous"]
	if batch == nil || len(batch.rows) != 1 || !batch.sent {
		t.Fatalf("expected one sent anomalous row, got %#v", batch)
	}
	row := batch.rows[0]
	// Append order: tenant, node, session, cluster, stream, name, duration, first, last, closed, reason, version, notes
	if row[0] != "tenant-1" || row[1] != "node-1" || row[2] != "session-1" || row[4] != "stream-1" {
		t.Errorf("natural key mismatch: %#v", row[:5])
	}
	if row[6] != uint32(300) {
		t.Errorf("estimated_duration = %#v, want 300", row[6])
	}
	if row[7] != int64(1_700_000_000_000) || row[8] != int64(1_700_000_300_000) {
		t.Errorf("observed window = %#v..%#v, want preserved", row[7], row[8])
	}
	if row[10] != "stale" {
		t.Errorf("closed_reason = %#v, want stale", row[10])
	}
	if v, ok := row[11].(int64); !ok || v <= 1_700_000_000_000 {
		t.Errorf("projection_version_ms = %#v, want a fresh wall-clock ms", row[11])
	}
	if notes, _ := row[12].(string); !strings.Contains(notes, "no USER_END within") {
		t.Errorf("notes = %q, want USER_END timeout annotation", notes)
	}
}

// TestStaleCloseViewerSessionsNoRowsDoesNotSend confirms the worker is a no-op
// when nothing is stale: the prepared batch is never sent, so an idle pass emits
// no anomalies.
func TestStaleCloseViewerSessionsNoRowsDoesNotSend(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	if err := h.staleCloseViewerSessions(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batch := conn.batches["periscope.viewer_sessions_anomalous"]; batch != nil && batch.sent {
		t.Fatalf("expected no send with zero stale rows, got %#v", batch.rows)
	}
}

// TestStaleCloseStreamSessionsClampsViewerSeconds covers the stream variant and
// its duration derivation: a positive viewer_seconds becomes the estimated
// duration, while a non-positive value clamps to zero rather than underflowing
// the uint32 column.
func TestStaleCloseStreamSessionsClampsViewerSeconds(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	// Scan columns: tenant, stream, node, observedFirstMs, observedLastMs, viewerSecondsMax.
	conn.addQueryRow("periscope.stream_state_current",
		"tenant-1", "stream-1", "node-1", int64(1_700_000_000_000), int64(1_700_000_600_000), int64(600))
	conn.addQueryRow("periscope.stream_state_current",
		"tenant-2", "stream-2", "node-2", int64(1_700_000_000_000), int64(1_700_000_600_000), int64(0))

	if err := h.staleCloseStreamSessions(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	batch := conn.batches["periscope.stream_sessions_anomalous"]
	if batch == nil || len(batch.rows) != 2 || !batch.sent {
		t.Fatalf("expected two sent anomalous rows, got %#v", batch)
	}
	// Append order: tenant, node, stream, cluster, name, duration, first, last, closed, reason, version, notes
	if batch.rows[0][5] != uint32(600) {
		t.Errorf("positive viewer_seconds duration = %#v, want 600", batch.rows[0][5])
	}
	if batch.rows[1][5] != uint32(0) {
		t.Errorf("zero viewer_seconds duration = %#v, want clamped 0", batch.rows[1][5])
	}
	if notes, _ := batch.rows[0][11].(string); !strings.Contains(notes, "no STREAM_END within") {
		t.Errorf("notes = %q, want STREAM_END timeout annotation", notes)
	}
}
