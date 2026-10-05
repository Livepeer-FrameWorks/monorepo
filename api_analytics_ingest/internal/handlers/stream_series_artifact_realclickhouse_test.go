//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
)

// A DVR, chapter or clip replayed after its stream ended runs as its own Mist
// stream. Foghorn resolves it to the artifact and names the source stream, so
// its lifecycle, buffer and track reports carry both. Those reports describe
// the replay, not the stream: the stream's current state, health series and
// rollups stay the stream's own, and the replay is kept under its artifact.
func TestArtifactReplayStaysOutOfTheStreamSeries_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-stream-series-artifact").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	const artifactHash = "20261005175800a1b2c3d4e5f60718"
	ended := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	deliver := func(eventType string, at time.Time, trigger *ipcpb.MistTrigger) {
		t.Helper()
		if err := h.HandleAnalyticsEvent(kafka.AnalyticsEvent{
			EventID: uuid.NewString(), EventType: eventType, Timestamp: at,
			Source: "decklog", TenantID: tenantID, Data: mustMistTriggerData(t, trigger),
		}); err != nil {
			t.Fatalf("%s: %v", eventType, err)
		}
	}
	u32 := func(v uint32) *uint32 { return &v }
	i32 := func(v int32) *int32 { return &v }
	recoverState := "RECOVER"

	// The live stream's last report: it ended.
	deliver("stream_lifecycle_update", ended, &ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: "edge-eu", StreamId: &streamID,
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
			NodeId: "edge-eu", InternalName: "live+rc18eu", Status: "offline", PrimaryBitrate: i32(3000),
			PrimaryWidth: i32(1920), PrimaryHeight: i32(1080),
		}},
	})

	// The replay of its DVR, minutes later, on another edge.
	hash := artifactHash
	for i := 0; i < 3; i++ {
		at := ended.Add(time.Duration(3+i) * time.Minute)
		deliver("stream_lifecycle_update", at, &ipcpb.MistTrigger{
			TriggerType: "STREAM_LIFECYCLE_UPDATE", NodeId: "edge-us", StreamId: &streamID, ArtifactHash: &hash,
			TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				NodeId: "edge-us", InternalName: "vod+" + artifactHash, Status: "live", BufferState: &recoverState,
				TotalViewers: u32(1),
			}},
		})
		deliver("stream_buffer", at, &ipcpb.MistTrigger{
			TriggerType: "STREAM_BUFFER", NodeId: "edge-us", StreamId: &streamID, ArtifactHash: &hash,
			TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{StreamBuffer: &ipcpb.StreamBufferTrigger{
				StreamName: "vod+" + artifactHash, BufferState: recoverState,
			}},
		})
	}
	deliver("stream_track_list", ended.Add(3*time.Minute), &ipcpb.MistTrigger{
		TriggerType: "LIVE_TRACK_LIST", NodeId: "edge-us", StreamId: &streamID, ArtifactHash: &hash,
		TriggerPayload: &ipcpb.MistTrigger_TrackList{TrackList: &ipcpb.StreamTrackListTrigger{
			StreamName: "vod+" + artifactHash, TotalTracks: i32(2), PrimaryHeight: i32(1080),
		}},
	})

	var status string
	if err := conn.QueryRow(ctx, `SELECT status FROM periscope.stream_state_current FINAL WHERE tenant_id = ? AND stream_id = ?`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID)).Scan(&status); err != nil {
		t.Fatalf("read stream state: %v", err)
	}
	if status != "offline" {
		t.Fatalf("stream state after a DVR replay = %q, want offline", status)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_health_samples WHERE tenant_id = ? AND stream_id = ?`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID)); got != 1 {
		t.Fatalf("stream health samples = %d, want only the stream's own 1", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_health_5m WHERE tenant_id = ? AND stream_id = ? AND node_id = 'edge-us'`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID)); got != 0 {
		t.Fatalf("stream health 5m holds %d replay rows", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_event_log WHERE tenant_id = ? AND stream_id = ? AND node_id = 'edge-us'`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID)); got != 0 {
		t.Fatalf("stream event log holds %d replay rows", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.track_list_events WHERE tenant_id = ? AND stream_id = ?`,
		uuid.MustParse(tenantID), uuid.MustParse(streamID)); got != 0 {
		t.Fatalf("stream track lists hold %d replay rows", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_viewer_5m WHERE tenant_id = ?`, uuid.MustParse(tenantID)); got != 1 {
		t.Fatalf("stream viewer 5m rows = %d, want only the stream's own", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.quality_tier_daily WHERE tenant_id = ?`, uuid.MustParse(tenantID)); got != 0 {
		t.Fatalf("quality tier daily holds %d replay rows", got)
	}

	// The replay is kept under its artifact.
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_health_samples WHERE tenant_id = ? AND artifact_hash = ?`,
		uuid.MustParse(tenantID), artifactHash); got != 6 {
		t.Fatalf("artifact health samples = %d, want 6 (3 lifecycle + 3 buffer)", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.stream_event_log WHERE tenant_id = ? AND artifact_hash = ?`,
		uuid.MustParse(tenantID), artifactHash); got != 7 {
		t.Fatalf("artifact event log rows = %d, want 7", got)
	}
	if got := countRows(t, conn, `SELECT count() FROM periscope.track_list_events WHERE tenant_id = ? AND artifact_hash = ?`,
		uuid.MustParse(tenantID), artifactHash); got != 1 {
		t.Fatalf("artifact track list rows = %d, want 1", got)
	}
}
