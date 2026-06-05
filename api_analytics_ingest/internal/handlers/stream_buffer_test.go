package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// mistTriggerEvent renders a MistTrigger into the kafka.AnalyticsEvent shape the
// ingest handlers parse (protojson round-trip into a Data map).
func mistTriggerEvent(t *testing.T, tenantID string, ts time.Time, mt *ipcpb.MistTrigger) kafka.AnalyticsEvent {
	t.Helper()
	raw, err := protojson.Marshal(mt)
	if err != nil {
		t.Fatalf("marshal MistTrigger: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal proto json: %v", err)
	}
	return kafka.AnalyticsEvent{
		EventID:   uuid.NewString(),
		EventType: "stream_buffer",
		Timestamp: ts,
		TenantID:  tenantID,
		Data:      data,
	}
}

// TestProcessStreamBuffer_MapsAndStripsPrefix pins the dual-write to
// stream_event_log + stream_health_samples and the key derivations: the
// live+/vod+ prefix is stripped to a clean internal_name, the event is tagged
// stream_buffer/live, and the primary video track's dimensions are surfaced.
func TestProcessStreamBuffer_MapsAndStripsPrefix(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	streamID := uuid.NewString()
	sb := &ipcpb.StreamBufferTrigger{
		StreamName:  "live+abc123",
		BufferState: "FULL",
		TrackCount:  proto.Int32(2),
		QualityTier: proto.String("1080p"),
		Tracks: []*ipcpb.StreamTrack{
			{TrackType: "video", Width: proto.Int32(1920), Height: proto.Int32(1080), Fps: proto.Float64(30), Codec: "H264", BitrateKbps: proto.Int32(5000)},
			{TrackType: "audio", Codec: "AAC", Channels: proto.Int32(2), SampleRate: proto.Int32(48000), BitrateKbps: proto.Int32(128)},
		},
	}
	mt := &ipcpb.MistTrigger{
		StreamId:       proto.String(streamID),
		NodeId:         "node-1",
		ClusterId:      proto.String("cluster-1"),
		TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{StreamBuffer: sb},
	}
	event := mistTriggerEvent(t, "tenant-1", time.Unix(1_700_000_000, 0).UTC(), mt)

	if err := h.processStreamBuffer(context.Background(), event); err != nil {
		t.Fatalf("processStreamBuffer: %v", err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("expected dual-write (stream_event_log + stream_health_samples), got %d rows", len(batch.rows))
	}
	if !batch.sendCalled {
		t.Fatal("expected batches sent")
	}

	// stream_event_log columns: timestamp, event_id, tenant_id, stream_id,
	// internal_name, node_id, cluster_id, event_type, status, ...
	ev := batch.rows[0]
	if ev[2] != "tenant-1" {
		t.Errorf("tenant_id = %v, want tenant-1", ev[2])
	}
	if ev[4] != "abc123" {
		t.Errorf("internal_name = %v, want abc123 (live+ stripped)", ev[4])
	}
	if ev[7] != "stream_buffer" || ev[8] != "live" {
		t.Errorf("event_type/status = %v/%v, want stream_buffer/live", ev[7], ev[8])
	}
	// primary_width column (index 14) is a *uint16 from the primary video track.
	if w, ok := ev[14].(*uint16); !ok || w == nil || *w != 1920 {
		t.Errorf("primary_width = %v, want *uint16(1920) from the video track", ev[14])
	}
}

func TestProcessStreamBuffer_RejectsWrongPayload(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	// Valid stream_id (so requireStreamID passes) but no StreamBuffer payload →
	// the payload type assertion must fail rather than write an empty fact.
	mt := &ipcpb.MistTrigger{StreamId: proto.String(uuid.NewString())}
	event := mistTriggerEvent(t, "tenant-1", time.Now(), mt)

	if err := h.processStreamBuffer(context.Background(), event); err == nil {
		t.Fatal("expected error for missing StreamBuffer payload")
	}
	if len(batch.rows) != 0 {
		t.Error("no fact should be written when the payload is wrong")
	}
}

func TestProcessStreamBuffer_RejectsMissingStreamID(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	// No valid stream_id anywhere → requireStreamID drops the event.
	mt := &ipcpb.MistTrigger{
		TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{StreamBuffer: &ipcpb.StreamBufferTrigger{StreamName: "live+x"}},
	}
	event := mistTriggerEvent(t, "tenant-1", time.Now(), mt)

	if err := h.processStreamBuffer(context.Background(), event); err == nil {
		t.Fatal("expected drop error for missing/invalid stream_id")
	}
}
