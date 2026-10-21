//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// One ingest session is projected into stream_runtime_5m twice: by the live
// branch while stream_state_current says live, and by the final branch once
// its STREAM_END lands. Mist reports a started_at one second after the
// stream_start event-log row the final projection resolves its start from.
// Both branches must write under one session key so the final rows replace
// the live rows, leaving the session's runtime counted once.
func TestStreamRuntimeLiveAndFinalShareOneSession_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-stream-runtime-session").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	const node, cluster, internal = "edge-a", "media-a", "runtime-session"
	tenantID, streamID := uuid.NewString(), uuid.NewString()
	startedAt := time.Now().UTC().Add(-time.Hour).Truncate(5 * time.Minute)
	endedAt := startedAt.Add(30 * time.Minute)

	if err := conn.Exec(ctx, `INSERT INTO periscope.stream_event_log
		(event_id, timestamp, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status, event_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'stream_start', 'live', '{}')`,
		uuid.New(), startedAt, uuid.MustParse(tenantID), uuid.MustParse(streamID), internal, node, cluster); err != nil {
		t.Fatalf("seed stream_start: %v", err)
	}
	if err := conn.Exec(ctx, `INSERT INTO periscope.stream_state_current
		(tenant_id, stream_id, internal_name, node_id, cluster_id, status, buffer_state,
		 current_viewers, total_inputs, uploaded_bytes, downloaded_bytes, viewer_seconds, started_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'live', 'FULL', 2, 1, 0, 0, 0, ?, ?)`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID), internal, node, cluster,
		startedAt.Add(time.Second), endedAt.Add(-time.Minute)); err != nil {
		t.Fatalf("seed live stream_state_current: %v", err)
	}

	liveWindowStart := startedAt.Add(15 * time.Minute)
	if err := h.rebuildStreamRuntime5m(ctx, liveWindowStart, liveWindowStart.Add(5*time.Minute)); err != nil {
		t.Fatalf("live runtime rebuild: %v", err)
	}

	projectionStart := time.Now().UTC().Add(-time.Second)
	endID := uuid.NewString()
	deliverStreamEnds(t, conn, h, false, streamEndProducer{
		decklogAt: endedAt.Add(time.Second),
		trigger: &ipcpb.MistTrigger{
			TriggerType: "STREAM_END", NodeId: node, Timestamp: endedAt.Unix(), RequestId: endID, EventId: endID,
			TenantId: proto.String(tenantID), StreamId: proto.String(streamID), ClusterId: proto.String(cluster),
			TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{
				StreamName: "live+" + internal, TotalViewers: proto.Int64(2), TriggerUnixMillis: endedAt.UnixMilli(),
			}},
		},
	})
	projectionEnd := time.Now().UTC().Add(time.Second)

	var finalStartMS int64
	if err := conn.QueryRow(ctx, `SELECT source_started_at_ms FROM periscope.stream_sessions_final_v
		WHERE tenant_id = ? AND stream_id = ?`, tenantID, streamID).Scan(&finalStartMS); err != nil {
		t.Fatalf("read final session: %v", err)
	}
	if finalStartMS != startedAt.UnixMilli() {
		t.Fatalf("final session start = %d, want the event-log start %d", finalStartMS, startedAt.UnixMilli())
	}

	if err := h.rebuildStreamRuntime5m(ctx, projectionStart, projectionEnd); err != nil {
		t.Fatalf("final runtime rebuild: %v", err)
	}

	var sessions, runtimeSeconds uint64
	if err := conn.QueryRow(ctx, `SELECT uniqExact(source_event_id), sum(active_seconds)
		FROM periscope.stream_runtime_5m_v
		WHERE tenant_id = ? AND stream_id = ?`, tenantID, streamID).Scan(&sessions, &runtimeSeconds); err != nil {
		t.Fatalf("read stream runtime: %v", err)
	}
	if sessions != 1 || runtimeSeconds != 1800 {
		t.Fatalf("stream_runtime_5m_v sessions=%d runtime=%ds, want 1 session of 1800s", sessions, runtimeSeconds)
	}
}
