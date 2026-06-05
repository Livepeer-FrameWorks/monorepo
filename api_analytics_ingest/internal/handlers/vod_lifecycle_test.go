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

// TestProcessVodLifecycle_MapsFactAndTenantOverride pins the dual-write
// (artifact_state_current + artifact_events), content_type=vod, vod_hash used as
// both request_id and internal_name, and the contract that the payload's
// tenant_id overrides the envelope tenant when present.
func TestProcessVodLifecycle_MapsFactAndTenantOverride(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	vod := &ipcpb.VodLifecycleData{
		VodHash:  "vodhash123",
		TenantId: proto.String("vod-tenant"), // overrides the envelope tenant
		Filename: proto.String("movie.mp4"),
	}
	mt := &ipcpb.MistTrigger{
		StreamId:       proto.String(uuid.NewString()),
		TriggerPayload: &ipcpb.MistTrigger_VodLifecycleData{VodLifecycleData: vod},
	}
	// Envelope tenant differs from the payload tenant on purpose.
	event := mistTriggerEvent(t, "envelope-tenant", time.Unix(1_700_000_000, 0).UTC(), mt)

	if err := h.processVodLifecycle(context.Background(), event); err != nil {
		t.Fatalf("processVodLifecycle: %v", err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("expected dual-write (artifact_state_current + artifact_events), got %d rows", len(batch.rows))
	}
	if !batch.sendCalled {
		t.Fatal("expected batches sent")
	}

	// artifact_state_current: tenant_id, stream_id, request_id, internal_name,
	// filename, content_type, stage, ...
	st := batch.rows[0]
	if st[0] != "vod-tenant" {
		t.Errorf("tenant_id = %v, want vod-tenant (payload overrides envelope)", st[0])
	}
	if st[2] != "vodhash123" || st[3] != "vodhash123" {
		t.Errorf("request_id/internal_name = %v/%v, want vodhash123 (both = vod_hash)", st[2], st[3])
	}
	if st[5] != "vod" {
		t.Errorf("content_type = %v, want vod", st[5])
	}
}

func TestProcessVodLifecycle_EnvelopeTenantWhenPayloadEmpty(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	// No payload tenant_id → falls back to the envelope tenant.
	vod := &ipcpb.VodLifecycleData{VodHash: "h2"}
	mt := &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_VodLifecycleData{VodLifecycleData: vod}}
	event := mistTriggerEvent(t, "envelope-tenant", time.Now(), mt)

	if err := h.processVodLifecycle(context.Background(), event); err != nil {
		t.Fatalf("processVodLifecycle: %v", err)
	}
	if batch.rows[0][0] != "envelope-tenant" {
		t.Errorf("tenant_id = %v, want envelope-tenant fallback", batch.rows[0][0])
	}
}

func TestProcessVodLifecycle_RejectsWrongPayload(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	mt := &ipcpb.MistTrigger{StreamId: proto.String(uuid.NewString())} // no VOD payload
	event := mistTriggerEvent(t, "tenant-1", time.Now(), mt)

	if err := h.processVodLifecycle(context.Background(), event); err == nil {
		t.Fatal("expected error for missing VodLifecycleData payload")
	}
	if len(batch.rows) != 0 {
		t.Error("no fact should be written when the payload is wrong")
	}
}
