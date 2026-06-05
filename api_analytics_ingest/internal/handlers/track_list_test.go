package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestProcessTrackList_MapsCountsAndStreamEvent pins the dual-write to
// track_list_events + stream_event_log, the live+ prefix strip, and the
// track-count projection (total/video/audio).
func TestProcessTrackList_MapsCountsAndStreamEvent(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	streamID := uuid.NewString()
	tl := &ipcpb.StreamTrackListTrigger{
		StreamName:      "live+xyz789",
		TotalTracks:     proto.Int32(3),
		VideoTrackCount: proto.Int32(1),
		AudioTrackCount: proto.Int32(2),
		Tracks: []*ipcpb.StreamTrack{
			{TrackType: "video", Width: proto.Int32(1280), Height: proto.Int32(720)},
			{TrackType: "audio", Codec: "AAC"},
		},
	}
	mt := &ipcpb.MistTrigger{
		StreamId:       proto.String(streamID),
		NodeId:         "node-1",
		ClusterId:      proto.String("cluster-1"),
		TriggerPayload: &ipcpb.MistTrigger_TrackList{TrackList: tl},
	}
	event := mistTriggerEvent(t, "tenant-1", time.Unix(1_700_000_000, 0).UTC(), mt)

	if err := h.processTrackList(context.Background(), event); err != nil {
		t.Fatalf("processTrackList: %v", err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("expected dual-write (track_list_events + stream_event_log), got %d rows", len(batch.rows))
	}
	if !batch.sendCalled {
		t.Fatal("expected batches sent")
	}

	// track_list_events: timestamp, event_id, tenant_id, stream_id, internal_name,
	// node_id, track_list, track_count, video_track_count, audio_track_count, ...
	tle := batch.rows[0]
	if tle[2] != "tenant-1" {
		t.Errorf("tenant_id = %v, want tenant-1", tle[2])
	}
	if tle[4] != "xyz789" {
		t.Errorf("internal_name = %v, want xyz789 (live+ stripped)", tle[4])
	}
	if tle[7] != uint16(3) || tle[8] != uint16(1) || tle[9] != uint16(2) {
		t.Errorf("track counts = total %v / video %v / audio %v, want 3/1/2", tle[7], tle[8], tle[9])
	}

	// Canonical stream event mirrors the lifecycle: event_type/status cols.
	se := batch.rows[1]
	if se[7] != "track_list_update" || se[8] != "live" {
		t.Errorf("stream event type/status = %v/%v, want track_list_update/live", se[7], se[8])
	}
}

func TestProcessTrackList_RejectsWrongPayload(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	mt := &ipcpb.MistTrigger{StreamId: proto.String(uuid.NewString())} // valid stream_id, no TrackList payload
	event := mistTriggerEvent(t, "tenant-1", time.Now(), mt)

	if err := h.processTrackList(context.Background(), event); err == nil {
		t.Fatal("expected error for missing TrackList payload")
	}
	if len(batch.rows) != 0 {
		t.Error("no fact should be written when the payload is wrong")
	}
}
