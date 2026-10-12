package handlers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func bufferedStateRow(t *testing.T, ctx context.Context, h *AnalyticsHandler, tenantID, streamID uuid.UUID) {
	t.Helper()
	w, err := periscopeingestdb.PrepareStreamLifecycleState(ctx, h.clickhouse)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := w.Append(periscopeingestdb.StreamLifecycleStateRow{TenantID: tenantID, StreamID: streamID, Status: "live", StartedAt: &now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := w.Send(); err != nil {
		t.Fatal(err)
	}
}

// A started_at lookup must observe the state row an earlier event of the same
// poll batch wrote for that stream, and must not flush for other streams.
func TestStartedAtLookupFlushesOnlyTheSameStreamsBufferedState(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := &AnalyticsHandler{clickhouse: clickhouseNativeConn{conn: conn}, logger: logging.NewLogger()}
	buffer := h.NewWriteBuffer(periscopeingestdb.WriteBufferOptions{})
	ctx := periscopeingestdb.WithWriteBuffer(context.Background(), buffer)
	tenantID, streamID, otherStream := uuid.New(), uuid.New(), uuid.New()

	bufferedStateRow(t, ctx, h, tenantID, otherStream)
	if _, _, err := h.lookupCurrentStreamStartedAt(ctx, tenantID.String(), streamID); err != nil {
		t.Fatal(err)
	}
	if conn.prepares["stream_state_current"] != 0 || buffer.Pending() != 1 {
		t.Fatalf("lookup for another stream flushed: prepares=%d pending=%d", conn.prepares["stream_state_current"], buffer.Pending())
	}

	bufferedStateRow(t, ctx, h, tenantID, streamID)
	if _, _, err := h.lookupCurrentLiveStreamStartedAt(ctx, tenantID.String(), streamID); err != nil {
		t.Fatal(err)
	}
	if conn.prepares["stream_state_current"] != 1 || buffer.Pending() != 0 {
		t.Fatalf("lookup did not flush the stream's buffered state first: prepares=%d pending=%d", conn.prepares["stream_state_current"], buffer.Pending())
	}
	if len(conn.queries) != 2 {
		t.Fatalf("queries = %d, want both lookups to read ClickHouse", len(conn.queries))
	}
}

// Under a write buffer the per-event duplicate lookup is skipped: a
// redelivered record's rows carry the ingest_id they had, and the flush drops
// them with one lookup per table.
func TestDuplicateCheckDoesNotQueryPerEventUnderAWriteBuffer(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := &AnalyticsHandler{clickhouse: clickhouseNativeConn{conn: conn}, logger: logging.NewLogger()}
	buffer := h.NewWriteBuffer(periscopeingestdb.WriteBufferOptions{})
	ctx := periscopeingestdb.WithWriteBuffer(context.Background(), buffer)
	event := kafka.AnalyticsEvent{EventID: uuid.NewString(), TenantID: uuid.NewString(), EventType: "viewer_connect", Timestamp: time.Now()}
	conn.addDuplicate("viewer_connection_events", uuid.MustParse(event.EventID))

	if h.isDuplicateEvent(ctx, "viewer_connection_events", event) {
		t.Fatal("buffered duplicate check reported a duplicate without the flush")
	}
	if len(conn.queries) != 0 {
		t.Fatalf("duplicate check queried ClickHouse %d times", len(conn.queries))
	}
}
