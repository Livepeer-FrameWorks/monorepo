//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// recordIdentityOrigin names one Kafka record the way the consumer's batch flusher does: by its
// key and value, so two producers of one fact are two records with distinct ingest_ids.
type recordIdentityOrigin struct{ identity string }

func (o *recordIdentityOrigin) IngestIdentity() string { return o.identity }

func newRecordOrigin(key string, value []byte) *recordIdentityOrigin {
	sum := sha256.New()
	_, _ = sum.Write([]byte(key))
	_, _ = sum.Write([]byte{0})
	_, _ = sum.Write(value)
	return &recordIdentityOrigin{identity: hex.EncodeToString(sum.Sum(nil))}
}

type streamEndProducer struct {
	trigger   *ipcpb.MistTrigger
	decklogAt time.Time
}

// deliverStreamEnd hands one producer's end to Periscope as Decklog publishes it: the typed
// stream_end event and the raw journal record, each its own Kafka record handled in its own poll
// batch, or both producers' records of one topic in one poll batch when sameBatch is set.
func deliverStreamEnds(t *testing.T, conn database.ClickHouseNativeConn, h *AnalyticsHandler, sameBatch bool, producers ...streamEndProducer) {
	t.Helper()
	ctx := context.Background()
	type record struct {
		event kafka.AnalyticsEvent
		raw   kafka.Message
	}
	records := make([]record, 0, len(producers))
	for _, p := range producers {
		rawJSON, err := protojson.Marshal(p.trigger)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err := json.Unmarshal(rawJSON, &data); err != nil {
			t.Fatal(err)
		}
		payload, err := proto.Marshal(p.trigger)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record{
			event: kafka.AnalyticsEvent{
				EventID: p.trigger.GetEventId(), EventType: "stream_end", Timestamp: p.decklogAt, Source: "foghorn",
				TenantID: p.trigger.GetTenantId(), Data: data,
			},
			raw: kafka.Message{Topic: "analytics.raw_mist_triggers", Key: []byte(p.trigger.GetRequestId()), Value: payload, Headers: map[string]string{
				"tenant_id": p.trigger.GetTenantId(), "source_event_id": p.trigger.GetRequestId(),
				"trigger_type": "STREAM_END", "node_id": p.trigger.GetNodeId(),
			}},
		})
	}
	run := func(handle func(ctx context.Context, r record) error) {
		var buffer *periscopeingestdb.WriteBuffer
		for i, r := range records {
			if buffer == nil || !sameBatch {
				buffer = h.NewWriteBuffer(periscopeingestdb.WriteBufferOptions{})
			}
			batchCtx := periscopeingestdb.WithWriteBuffer(ctx, buffer)
			if err := handle(batchCtx, r); err != nil {
				t.Fatalf("producer %d: %v", i, err)
			}
			if !sameBatch || i == len(records)-1 {
				if err := buffer.Flush(ctx); err != nil {
					t.Fatalf("flush producer %d: %v", i, err)
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	run(func(ctx context.Context, r record) error {
		eventJSON, err := json.Marshal(r.event)
		if err != nil {
			return err
		}
		return h.HandleAnalyticsEventContext(periscopeingestdb.WithRowOrigin(ctx, newRecordOrigin(r.event.EventID, eventJSON)), r.event)
	})
	run(func(ctx context.Context, r record) error {
		return h.HandleRawMistTriggerMessage(periscopeingestdb.WithRowOrigin(ctx, newRecordOrigin(string(r.raw.Key), r.raw.Value)), r.raw)
	})
}

// One ingest session's end reaches Periscope from Mist's STREAM_END, from Foghorn's STREAM_END
// inferred from the poller's vanish, or from both in either order under the same session end id.
// Every combination leaves one stream_end event and one logical stream_sessions_final row, and
// whenever Mist's STREAM_END arrived its counters are the row's and its payload is the journaled one.
func TestStreamEndFromEitherProducerLandsOnce_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-stream-end-producers").Native
	h := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()

	const node, cluster, internal = "edge-a", "media-a", "end-producers"
	endedAt := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)

	for _, tc := range []struct {
		name      string
		order     []string
		sameBatch bool
		wantBytes int64
	}{
		{name: "inferred only", order: []string{"inferred"}, wantBytes: 0},
		{name: "mist only", order: []string{"mist"}, wantBytes: 4096},
		{name: "mist then inferred", order: []string{"mist", "inferred"}, wantBytes: 4096},
		{name: "inferred then mist", order: []string{"inferred", "mist"}, wantBytes: 4096},
		{name: "mist then inferred in one poll batch", order: []string{"mist", "inferred"}, sameBatch: true, wantBytes: 4096},
		{name: "inferred redelivered", order: []string{"inferred", "inferred"}, wantBytes: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tenantID, streamID := uuid.NewString(), uuid.NewString()
			endID := uuid.NewString()
			if err := conn.Exec(ctx, `INSERT INTO periscope.stream_event_log
				(event_id, timestamp, tenant_id, stream_id, internal_name, node_id, cluster_id, event_type, status, event_data)
				VALUES (?, ?, ?, ?, ?, ?, ?, 'stream_start', 'live', '{}')`,
				uuid.New(), endedAt.Add(-10*time.Minute), uuid.MustParse(tenantID), uuid.MustParse(streamID), internal, node, cluster); err != nil {
				t.Fatalf("seed stream_start: %v", err)
			}
			envelope := func(edgeAt time.Time, end *ipcpb.StreamEndTrigger) *ipcpb.MistTrigger {
				return &ipcpb.MistTrigger{
					TriggerType: "STREAM_END", NodeId: node, Timestamp: edgeAt.Unix(), RequestId: endID, EventId: endID,
					TenantId: proto.String(tenantID), StreamId: proto.String(streamID), ClusterId: proto.String(cluster),
					TriggerPayload: &ipcpb.MistTrigger_StreamEnd{StreamEnd: end},
				}
			}
			producers := map[string]streamEndProducer{
				"mist": {decklogAt: endedAt.Add(time.Second), trigger: envelope(endedAt, &ipcpb.StreamEndTrigger{
					StreamName: "live+" + internal, UploadedBytes: proto.Int64(4096), TotalViewers: proto.Int64(3),
					ViewerSeconds: proto.Int64(120), TriggerUnixMillis: endedAt.UnixMilli(),
				})},
				"inferred": {decklogAt: endedAt.Add(3 * time.Second), trigger: envelope(endedAt.Add(2*time.Second), &ipcpb.StreamEndTrigger{
					StreamName: "live+" + internal, TriggerUnixMillis: endedAt.Add(2 * time.Second).UnixMilli(), Inferred: true,
				})},
			}
			ordered := make([]streamEndProducer, 0, len(tc.order))
			for _, name := range tc.order {
				ordered = append(ordered, producers[name])
			}
			deliverStreamEnds(t, conn, h, tc.sameBatch, ordered...)

			var endEvents uint64
			if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.stream_event_log
				WHERE tenant_id = ? AND stream_id = ? AND event_type = 'stream_end'`, tenantID, streamID).Scan(&endEvents); err != nil {
				t.Fatal(err)
			}
			if endEvents != 1 {
				t.Fatalf("stream_event_log holds %d stream_end rows, want 1", endEvents)
			}
			var finals uint64
			var uploaded int64
			if err := conn.QueryRow(ctx, `SELECT count(), max(uploaded_bytes) FROM periscope.stream_sessions_final_v
				WHERE tenant_id = ? AND stream_id = ?`, tenantID, streamID).Scan(&finals, &uploaded); err != nil {
				t.Fatal(err)
			}
			if finals != 1 || uploaded != tc.wantBytes {
				t.Fatalf("stream_sessions_final_v rows=%d uploaded_bytes=%d, want 1 row with %d", finals, uploaded, tc.wantBytes)
			}
			var anomalous uint64
			if err := conn.QueryRow(ctx, `SELECT count() FROM periscope.stream_sessions_anomalous
				WHERE tenant_id = ? AND stream_id = ?`, tenantID, streamID).Scan(&anomalous); err != nil {
				t.Fatal(err)
			}
			if anomalous != 0 {
				t.Fatalf("stream_sessions_anomalous holds %d rows, want none", anomalous)
			}
			var journaled string
			if err := conn.QueryRow(ctx, `SELECT argMax(payload, ingested_at_ms) FROM periscope.raw_mist_triggers
				WHERE node_id = ? AND trigger_type = 'STREAM_END' AND source_request_id = ?`, node, endID).Scan(&journaled); err != nil {
				t.Fatal(err)
			}
			var audit ipcpb.MistTrigger
			if err := proto.Unmarshal([]byte(journaled), &audit); err != nil {
				t.Fatalf("decode journaled STREAM_END: %v", err)
			}
			if wantInferred := tc.wantBytes == 0; audit.GetStreamEnd().GetInferred() != wantInferred {
				t.Fatalf("raw_mist_triggers journals inferred=%v, want %v", audit.GetStreamEnd().GetInferred(), wantInferred)
			}
		})
	}
}
