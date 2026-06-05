package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

// qoeEvent wraps a MistTrigger as the AnalyticsEvent the processor receives:
// Data is the protobuf-as-JSON map, exactly as parseProtobufData expects. The
// tenantID is the Kafka-envelope (Bridge-derived) attribution, deliberately set
// independently of anything inside the trigger body.
func qoeEvent(t *testing.T, tenantID string, trigger *ipcpb.MistTrigger) kafka.AnalyticsEvent {
	t.Helper()
	raw, err := protojson.Marshal(trigger)
	if err != nil {
		t.Fatalf("marshal trigger: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("trigger to map: %v", err)
	}
	return kafka.AnalyticsEvent{
		EventID:       uuid.NewString(),
		EventType:     trigger.GetTriggerType(),
		TenantID:      tenantID,
		Timestamp:     time.Now(),
		SchemaVersion: 2,
		Data:          data,
	}
}

// The browser is untrusted: a spoofed tenant_id in the QoE body must be ignored
// in favor of the Kafka envelope's server-derived tenant, the (content,session,
// beacon_seq) dedupe key must be carried, and the additive deltas must be stored
// verbatim (the processor stores, it does not sum).
func TestProcessPlaybackSessionQoe_ServerDerivedAttributionAndDeltas(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	realTenant := uuid.NewString()
	streamID := uuid.NewString()
	trigger := &ipcpb.MistTrigger{
		TriggerType: "playback_session_qoe",
		TriggerPayload: &ipcpb.MistTrigger_PlaybackSessionQoe{
			PlaybackSessionQoe: &ipcpb.PlaybackSessionQoe{
				TenantId:            stringPtr("evil-spoofed-tenant"), // must be ignored
				StreamId:            stringPtr(streamID),
				InternalName:        "live+demo",
				ContentId:           "content-xyz",
				SessionId:           "sess-1",
				BeaconSeq:           3,
				IsFinal:             true,
				FlushReason:         "visibility_hidden",
				ClusterAttributed:   true,
				IsLive:              true,
				PlayedMs:            42000,
				RebufferMs:          1200,
				RebufferCount:       2,
				SeekWaitMs:          800,
				FrameStatsSupported: true,
				FramesDecoded:       2500,
				FramesDropped:       7,
			},
		},
	}
	event := qoeEvent(t, realTenant, trigger)

	if err := h.processPlaybackSessionQoe(context.Background(), event); err != nil {
		t.Fatalf("processPlaybackSessionQoe: %v", err)
	}

	batch := conn.batches["client_qoe_session_deltas"]
	if batch == nil || len(batch.rows) != 1 || !batch.sent {
		t.Fatalf("expected 1 sent delta row, got %#v", batch)
	}
	row := batch.rows[0]

	if row[2] != realTenant {
		t.Errorf("tenant_id = %v, want envelope tenant %q (spoof must be ignored)", row[2], realTenant)
	}
	if got, ok := row[3].(uuid.UUID); !ok || got.String() != streamID {
		t.Errorf("stream_id = %#v, want %q", row[3], streamID)
	}
	if row[5] != "demo" {
		t.Errorf("internal_name = %v, want extracted %q", row[5], "demo")
	}
	if row[6] != "content-xyz" || row[7] != "sess-1" {
		t.Errorf("content/session = %v/%v", row[6], row[7])
	}
	if row[8] != uint32(3) {
		t.Errorf("beacon_seq = %#v, want 3 (dedupe key must be preserved)", row[8])
	}
	if row[9] != uint8(1) || row[10] != "visibility_hidden" {
		t.Errorf("is_final/flush_reason = %#v/%v", row[9], row[10])
	}
	if row[14] != uint8(1) {
		t.Errorf("cluster_attributed = %#v, want 1", row[14])
	}
	// Additive deltas stored verbatim.
	if row[21] != uint64(42000) || row[22] != uint64(1200) || row[23] != uint32(2) || row[24] != uint64(800) {
		t.Errorf("deltas not verbatim: played=%#v reb=%#v cnt=%#v seek=%#v", row[21], row[22], row[23], row[24])
	}
	if row[25] != uint8(1) || row[26] != uint64(2500) || row[27] != uint64(7) {
		t.Errorf("frame deltas: supported=%#v dec=%#v drop=%#v", row[25], row[26], row[27])
	}
	// Live beacon → no VOD geometry/reach (bucket_width_s/asset_duration_s/max_bucket_reached all 0).
	if row[37] != uint32(0) || row[38] != uint32(0) || row[39] != uint32(0) {
		t.Errorf("vod geometry/reach should be 0 on a live beacon: bw=%#v dur=%#v reach=%#v", row[37], row[38], row[39])
	}
	if row[43] != uint8(2) {
		t.Errorf("schema_version = %#v, want 2", row[43])
	}

	// No retention histogram on a live beacon → no vod batch.
	if conn.batches["vod_retention_buckets"] != nil {
		t.Errorf("live beacon must not write vod_retention_buckets")
	}
}

// A VOD beacon carries a sparse retention histogram that must fan out to one
// row per bucket, each tagged with the same beacon_seq/content/session, and the
// stream_id must be nil for a standalone VOD artifact.
func TestProcessPlaybackSessionQoe_VodRetentionFanOut(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	trigger := &ipcpb.MistTrigger{
		TriggerType: "playback_session_qoe",
		TriggerPayload: &ipcpb.MistTrigger_PlaybackSessionQoe{
			PlaybackSessionQoe: &ipcpb.PlaybackSessionQoe{
				ArtifactHash:            "art-1",
				ContentType:             "vod",
				ContentId:               "vod-xyz",
				SessionId:               "sess-2",
				BeaconSeq:               4,
				BucketWidthS:            10,
				AssetDurationS:          30,
				RetentionBuckets:        []uint32{0, 1, 2},
				RetentionSecondsWatched: []float32{10, 5, 2},
			},
		},
	}
	event := qoeEvent(t, uuid.NewString(), trigger)

	if err := h.processPlaybackSessionQoe(context.Background(), event); err != nil {
		t.Fatalf("processPlaybackSessionQoe: %v", err)
	}

	// VOD artifact → nil stream_id on the delta row.
	if row := conn.batches["client_qoe_session_deltas"].rows[0]; row[3] != nil {
		t.Errorf("vod delta stream_id = %#v, want nil", row[3])
	}

	vod := conn.batches["vod_retention_buckets"]
	if vod == nil || len(vod.rows) != 3 || !vod.sent {
		t.Fatalf("expected 3 sent retention rows, got %#v", vod)
	}
	wantBucket := []uint32{0, 1, 2}
	wantSeconds := []float32{10, 5, 2}
	for i, row := range vod.rows {
		if row[5] != "vod-xyz" || row[6] != "sess-2" || row[7] != uint32(4) {
			t.Errorf("row %d identity = content=%v session=%v seq=%#v", i, row[5], row[6], row[7])
		}
		if row[10] != wantBucket[i] || row[11] != wantSeconds[i] {
			t.Errorf("row %d bucket=%#v seconds=%#v, want %d/%v", i, row[10], row[11], wantBucket[i], wantSeconds[i])
		}
	}
}

// Defensive guard: parallel retention arrays of mismatched length are dropped
// rather than mis-paired — the delta row is still written.
func TestProcessPlaybackSessionQoe_MismatchedRetentionDropped(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	trigger := &ipcpb.MistTrigger{
		TriggerType: "playback_session_qoe",
		TriggerPayload: &ipcpb.MistTrigger_PlaybackSessionQoe{
			PlaybackSessionQoe: &ipcpb.PlaybackSessionQoe{
				ContentId:               "vod-xyz",
				SessionId:               "sess-3",
				RetentionBuckets:        []uint32{0, 1},
				RetentionSecondsWatched: []float32{10}, // length mismatch
			},
		},
	}
	event := qoeEvent(t, uuid.NewString(), trigger)

	if err := h.processPlaybackSessionQoe(context.Background(), event); err != nil {
		t.Fatalf("processPlaybackSessionQoe: %v", err)
	}
	if conn.batches["vod_retention_buckets"] != nil {
		t.Errorf("mismatched arrays must not write any retention rows")
	}
	if b := conn.batches["client_qoe_session_deltas"]; b == nil || len(b.rows) != 1 {
		t.Errorf("delta row should still be written, got %#v", b)
	}
}

func TestProcessPlaybackSessionQoe_WrongPayloadErrors(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	streamID := uuid.NewString()
	trigger := &ipcpb.MistTrigger{
		TriggerType:    "playback_session_qoe",
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: &ipcpb.StreamEndTrigger{StreamId: &streamID}},
	}
	event := qoeEvent(t, uuid.NewString(), trigger)

	if err := h.processPlaybackSessionQoe(context.Background(), event); err == nil {
		t.Error("expected error for a non-QoE payload")
	}
}

// The boot waterfall headline must take the FIRST manifest and FIRST
// first_segment resource (the `if url == ""` guard), pull cache status/age from
// the mist_json resource, and preserve the full resource array as JSON.
// Attribution is server-derived from the envelope, internal_name is extracted.
func TestProcessPlaybackBootTrace_ResourceHeadlineFirstWins(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	realTenant := uuid.NewString()
	streamID := uuid.NewString()
	trigger := &ipcpb.MistTrigger{
		TriggerType: "playback_boot",
		TriggerPayload: &ipcpb.MistTrigger_PlaybackBootTrace{
			PlaybackBootTrace: &ipcpb.PlaybackBootTrace{
				TenantId:          stringPtr("evil-spoofed-tenant"),
				StreamId:          stringPtr(streamID),
				InternalName:      "vod+clip",
				ClusterAttributed: true,
				IsLive:            false,
				TotalTtfMs:        1234,
				Resources: []*ipcpb.PlaybackBootResource{
					{Kind: "manifest", Url: "m1", DurationMs: 10, TransferSize: 100},
					{Kind: "manifest", Url: "m2", DurationMs: 20, TransferSize: 200},
					{Kind: "first_segment", Url: "s1", DurationMs: 5, TransferSize: 50},
					{Kind: "first_segment", Url: "s2", DurationMs: 6},
					{Kind: "mist_json", CacheStatus: stringPtr("HIT"), AgeSeconds: uint32Ptr(42)},
				},
			},
		},
	}
	event := qoeEvent(t, realTenant, trigger)

	if err := h.processPlaybackBootTrace(context.Background(), event); err != nil {
		t.Fatalf("processPlaybackBootTrace: %v", err)
	}
	batch := conn.batches["player_boot_samples"]
	if batch == nil || len(batch.rows) != 1 || !batch.sent {
		t.Fatalf("expected 1 sent boot row, got %#v", batch)
	}
	row := batch.rows[0]

	if row[2] != realTenant {
		t.Errorf("tenant_id = %v, want envelope tenant %q", row[2], realTenant)
	}
	if row[5] != "clip" {
		t.Errorf("internal_name = %v, want extracted %q", row[5], "clip")
	}
	if row[11] != uint8(1) {
		t.Errorf("cluster_attributed = %#v, want 1", row[11])
	}
	if row[12] != uint32(1234) {
		t.Errorf("total_ttf_ms = %#v, want 1234", row[12])
	}
	if row[23] != uint8(0) {
		t.Errorf("is_live = %#v, want 0", row[23])
	}
	// First manifest wins.
	if row[26] != "m1" || row[27] != uint32(10) || row[28] != uint64(100) {
		t.Errorf("manifest headline = url=%v ms=%#v size=%#v, want m1/10/100", row[26], row[27], row[28])
	}
	// First first_segment wins.
	if row[29] != "s1" || row[30] != uint32(5) {
		t.Errorf("first_segment headline = url=%v ms=%#v, want s1/5", row[29], row[30])
	}
	if row[32] != "HIT" {
		t.Errorf("cdn_cache_status = %v, want HIT", row[32])
	}
	if age, ok := row[33].(*uint32); !ok || age == nil || *age != 42 {
		t.Errorf("age_seconds = %#v, want *uint32(42)", row[33])
	}
	if s, ok := row[34].(string); !ok || !strings.Contains(s, "first_segment") {
		t.Errorf("resources JSON should round-trip the full array, got %#v", row[34])
	}
	if row[38] != uint8(2) {
		t.Errorf("schema_version = %#v, want 2", row[38])
	}
}

// With no resources the headline fields stay empty and resources serializes to
// an empty array (not null) — the row is still written.
func TestProcessPlaybackBootTrace_EmptyResources(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)

	trigger := &ipcpb.MistTrigger{
		TriggerType: "playback_boot",
		TriggerPayload: &ipcpb.MistTrigger_PlaybackBootTrace{
			PlaybackBootTrace: &ipcpb.PlaybackBootTrace{SessionId: "sess-9", Outcome: "success"},
		},
	}
	event := qoeEvent(t, uuid.NewString(), trigger)

	if err := h.processPlaybackBootTrace(context.Background(), event); err != nil {
		t.Fatalf("processPlaybackBootTrace: %v", err)
	}
	row := conn.batches["player_boot_samples"].rows[0]
	if row[26] != "" {
		t.Errorf("manifest_url = %v, want empty", row[26])
	}
	if row[34] != "[]" {
		t.Errorf("resources = %v, want %q", row[34], "[]")
	}
}
