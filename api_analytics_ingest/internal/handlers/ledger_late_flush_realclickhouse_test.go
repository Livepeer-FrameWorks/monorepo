//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// The consumer buffers a poll batch's rows and inserts them when the batch
// ends, retrying a failed insert until ClickHouse takes it. The ledger
// cursor and the billing cursor can pass the time a final was handled while
// its row is still unflushed; the row must still reach the ledger and the
// billing cursor once it lands.
func TestFinalFlushedAfterTheCursorPassedItsHandlingIsStillLedgered_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-ledger-late-flush").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	const node, sessionID, sourceID = "edge-late-flush", "late-flush-session", "late-flush-user-end"
	endedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	trigger := &ipcpb.MistTrigger{
		NodeId: node, TriggerType: "USER_END", RequestId: sourceID,
		Timestamp: endedAt.UnixMilli(), TenantId: proto.String(tenantID), ClusterId: proto.String("cluster-late"),
		StreamId: proto.String(streamID),
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
			StreamName: "live+late", StreamId: proto.String(streamID), SessionId: sessionID,
			Connector: "hls", Host: "203.0.113.9", Duration: 300, UpBytes: 1 << 20, DownBytes: 1 << 30,
		}},
	}
	payload, err := proto.Marshal(trigger)
	if err != nil {
		t.Fatal(err)
	}
	message := kafka.Message{Topic: "analytics.raw_mist_triggers", Key: []byte(sourceID), Value: payload, Headers: map[string]string{
		"tenant_id": tenantID, "cluster_id": "cluster-late", "source_event_id": sourceID,
		"trigger_type": "USER_END", "node_id": node,
	}}

	buffer := h.NewWriteBuffer(periscopeingestdb.WriteBufferOptions{})
	batchCtx := periscopeingestdb.WithRowOrigin(periscopeingestdb.WithWriteBuffer(ctx, buffer), newRecordOrigin(sourceID, payload))
	handledFrom := time.Now()
	if err := h.HandleRawMistTriggerMessage(batchCtx, message); err != nil {
		t.Fatalf("handle USER_END: %v", err)
	}

	// While the batch is unflushed, the ledger cursor moves past the time the
	// final was handled.
	cursor := time.Now().Add(time.Millisecond)
	if err := h.rebuildViewerUsage5m(ctx, handledFrom.Add(-time.Minute), cursor); err != nil {
		t.Fatalf("ledger chunk before the flush: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := buffer.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if err := h.rebuildViewerUsage5m(ctx, cursor, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("ledger chunk after the flush: %v", err)
	}

	tenantUUID := uuid.MustParse(tenantID)
	var ledgerSeconds uint64
	if err := conn.QueryRow(ctx, `SELECT sum(seconds_observed) FROM periscope.viewer_usage_5m_v
		WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantUUID, node, sessionID).Scan(&ledgerSeconds); err != nil {
		t.Fatal(err)
	}
	if ledgerSeconds != 300 {
		t.Errorf("viewer_usage_5m_v seconds=%d, want the flushed session's 300", ledgerSeconds)
	}
	var billableAtMS int64
	if err := conn.QueryRow(ctx, `SELECT billable_at_ms FROM periscope.viewer_sessions_final_v
		WHERE tenant_id = ? AND node_id = ? AND session_id = ?`, tenantUUID, node, sessionID).Scan(&billableAtMS); err != nil {
		t.Fatal(err)
	}
	if billableAtMS < cursor.UnixMilli() {
		t.Errorf("viewer_sessions_final billable_at_ms=%d precedes the billing cursor %d that passed before the row landed", billableAtMS, cursor.UnixMilli())
	}
}
