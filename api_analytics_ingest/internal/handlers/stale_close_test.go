package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func TestRestreamStatusPersistsSanitizedCurrentObservation(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.NewString()
	streamID := uuid.NewString()
	generationID := uuid.NewString()
	targetID := uuid.NewString()
	eventTime := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	err := h.HandleAnalyticsEvent(kafka.AnalyticsEvent{
		EventID: uuid.NewString(), EventType: "restream_status", Timestamp: eventTime,
		TenantID: tenantID, SourceClusterID: "cluster-a",
		Data: map[string]interface{}{
			"targetId": targetID, "streamId": streamID, "streamName": "live+demo",
			"sourceGeneration": generationID, "targetRevision": float64(3), "mistPushId": float64(17),
			"tenantId": tenantID, "nodeId": "node-a", "platform": " YouTube ",
			"state": "RESTREAM_STATE_PUSHING", "startedAtMs": float64(eventTime.Add(-time.Minute).UnixMilli()),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	batch := conn.batches["periscope.restream_sessions_current"]
	if batch == nil || len(batch.rows) != 1 || !batch.sent {
		t.Fatalf("current restream observation was not persisted: %#v", batch)
	}
	row := batch.rows[0]
	if row[0] != uuid.MustParse(tenantID) || row[1] != "node-a" || row[2] != "cluster-a" || row[3] != uuid.MustParse(streamID) || row[5] != uuid.MustParse(generationID) || row[6] != uuid.MustParse(targetID) {
		t.Fatalf("current restream identity mismatch: %#v", row)
	}
	if row[9] != "youtube" || row[10] != "pushing" || row[12] != eventTime.UnixMilli() {
		t.Fatalf("current restream state mismatch: %#v", row)
	}
}

// TestStaleCloseViewerSessionsEmitsAnomalyRow proves the core invariant: a stale
// live viewer session (returned by the scan) is materialized into one
// viewer_sessions_anomalous row that preserves its natural key, carries the
// observed duration/window, and is stamped "stale" with a fresh
// projection_version_ms. The scan also excludes keys already recorded in the
// anomaly table so periodic passes do not append the same logical anomaly.
func TestStaleCloseViewerSessionsEmitsAnomalyRow(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	streamID := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	// Scan columns: tenant, stream, session, node, cluster, observedFirstMs, observedLastMs, duration.
	conn.addQueryRow("periscope.client_qoe_samples",
		tenantID.String(), streamID.String(), "session-1", "node-1", "cluster-a",
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
	if row[0] != tenantID || row[1] != "node-1" || row[2] != "session-1" || row[3] != "cluster-a" || row[4] != streamID {
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
	if len(conn.queries) != 1 || !strings.Contains(conn.queries[0].query, "FROM periscope.viewer_sessions_anomalous") {
		t.Fatalf("viewer scan must exclude existing anomaly keys, queries=%#v", conn.queries)
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
	tenantOne := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	streamOne := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	tenantTwo := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	streamTwo := uuid.MustParse("44444444-4444-4444-8444-444444444444")

	// Scan columns: tenant, stream, node, observedFirstMs, observedLastMs, viewerSecondsMax.
	conn.addQueryRow("periscope.stream_state_current",
		tenantOne.String(), streamOne.String(), "node-1", int64(1_700_000_000_000), int64(1_700_000_600_000), int64(600))
	conn.addQueryRow("periscope.stream_state_current",
		tenantTwo.String(), streamTwo.String(), "node-2", int64(1_700_000_000_000), int64(1_700_000_600_000), int64(0))

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
	if len(conn.queries) != 1 || !strings.Contains(conn.queries[0].query, "FROM periscope.stream_sessions_anomalous") {
		t.Fatalf("stream scan must exclude existing anomaly keys, queries=%#v", conn.queries)
	}
}

func TestStaleCloseRestreamSessionsEmitsNonBillableAnomaly(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	tenantID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	streamID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	generationID := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	targetID := uuid.MustParse("44444444-4444-4444-8444-444444444444")
	conn.addQueryRow("periscope.restream_sessions_current",
		tenantID.String(), "node-1", "cluster-a", streamID.String(), "live+demo",
		generationID.String(), targetID.String(), int64(7), "youtube",
		int64(1_700_000_000_000), int64(1_700_000_300_000), "safe-payload")

	if err := h.staleCloseRestreamSessions(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch := conn.batches["periscope.restream_sessions_anomalous"]
	if batch == nil || len(batch.rows) != 1 || !batch.sent {
		t.Fatalf("expected one restream anomaly, got %#v", batch)
	}
	row := batch.rows[0]
	if row[0] != tenantID || row[1] != "node-1" || row[4] != streamID || row[5] != generationID || row[6] != targetID || row[7] != int64(7) {
		t.Fatalf("restream anomaly identity mismatch: %#v", row)
	}
	if row[10] != "stale" || !strings.Contains(row[11].(string), "no RESTREAM_STATUS_FINAL within") {
		t.Fatalf("restream anomaly is not marked stale: %#v", row)
	}
	if len(conn.queries) != 1 || !strings.Contains(conn.queries[0].query, "FROM periscope.restream_sessions_final") || !strings.Contains(conn.queries[0].query, "FROM periscope.restream_sessions_anomalous") {
		t.Fatalf("stale scan must exclude final and prior anomalous identities: %#v", conn.queries)
	}
}

// TestStaleMarkStreamStateOffline locks the status backstop's contract: one
// atomic INSERT ... SELECT against stream_state_current that only targets
// rows still claiming a non-terminal status, skips rows without a real
// stream UUID, and uses the 2-minute staleness window (12 missed 10s
// lifecycle refreshes) — not the 4h anomaly-accounting timeout.
func TestStaleMarkStreamStateOffline(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	if err := h.staleMarkStreamStateOffline(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(conn.execs) != 1 {
		t.Fatalf("expected exactly one exec, got %d: %#v", len(conn.execs), conn.execs)
	}
	q := conn.execs[0].query
	if !strings.Contains(q, "INSERT INTO periscope.stream_state_current") {
		t.Errorf("backstop must insert into stream_state_current, got %q", q)
	}
	if !strings.Contains(q, "'offline'") {
		t.Errorf("backstop must write status 'offline', got %q", q)
	}
	if !strings.Contains(q, "NOT IN ('offline', 'stopped', 'gone')") {
		t.Errorf("backstop must exclude terminal statuses, got %q", q)
	}
	if !strings.Contains(q, "stream_id != toUUIDOrZero('')") {
		t.Errorf("backstop must skip rows without a stream UUID, got %q", q)
	}
	if !strings.Contains(q, "INTERVAL 120 SECOND") {
		t.Errorf("backstop must use the 120s status window, got %q", q)
	}
}
