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

// TestProcessClipLifecycle_PrefersClipHash pins content_type=clip, the live+
// prefix strip on the internal name, and the request_id identity preferring
// clip_hash.
func TestProcessClipLifecycle_PrefersClipHash(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	cl := &ipcpb.ClipLifecycleData{
		StreamInternalName: proto.String("live+clipstream"),
		ClipHash:           "cliphash1",
		RequestId:          proto.String("req-1"),
	}
	mt := &ipcpb.MistTrigger{
		StreamId:       proto.String(uuid.NewString()),
		TriggerPayload: &ipcpb.MistTrigger_ClipLifecycleData{ClipLifecycleData: cl},
	}
	event := mistTriggerEvent(t, "tenant-1", time.Unix(1_700_000_000, 0).UTC(), mt)

	if err := h.processClipLifecycle(context.Background(), event); err != nil {
		t.Fatalf("processClipLifecycle: %v", err)
	}
	if len(batch.rows) < 1 || !batch.sendCalled {
		t.Fatalf("expected at least one fact written + Send, got rows=%d send=%v", len(batch.rows), batch.sendCalled)
	}

	// artifact_state_current: tenant_id, stream_id, request_id, internal_name,
	// filename, content_type, ...
	st := batch.rows[0]
	if st[0] != "tenant-1" {
		t.Errorf("tenant_id = %v, want tenant-1", st[0])
	}
	if st[2] != "cliphash1" {
		t.Errorf("request_id = %v, want cliphash1 (clip_hash preferred)", st[2])
	}
	if st[3] != "clipstream" {
		t.Errorf("internal_name = %v, want clipstream (live+ stripped)", st[3])
	}
	if st[5] != "clip" {
		t.Errorf("content_type = %v, want clip", st[5])
	}
}

func TestProcessClipLifecycle_FallsBackToRequestID(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	// No clip_hash → request_id becomes the canonical artifact identifier.
	cl := &ipcpb.ClipLifecycleData{StreamInternalName: proto.String("s"), RequestId: proto.String("req-fallback")}
	mt := &ipcpb.MistTrigger{
		StreamId:       proto.String(uuid.NewString()),
		TriggerPayload: &ipcpb.MistTrigger_ClipLifecycleData{ClipLifecycleData: cl},
	}
	event := mistTriggerEvent(t, "tenant-1", time.Now(), mt)

	if err := h.processClipLifecycle(context.Background(), event); err != nil {
		t.Fatalf("processClipLifecycle: %v", err)
	}
	if batch.rows[0][2] != "req-fallback" {
		t.Errorf("request_id = %v, want req-fallback (clip_hash empty)", batch.rows[0][2])
	}
}

func TestProcessClipLifecycle_RejectsWrongPayload(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	mt := &ipcpb.MistTrigger{StreamId: proto.String(uuid.NewString())} // no clip payload
	event := mistTriggerEvent(t, "tenant-1", time.Now(), mt)

	if err := h.processClipLifecycle(context.Background(), event); err == nil {
		t.Fatal("expected error for missing ClipLifecycleData payload")
	}
	if len(batch.rows) != 0 {
		t.Error("no fact should be written when the payload is wrong")
	}
}
