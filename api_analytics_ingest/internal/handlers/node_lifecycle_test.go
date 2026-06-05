package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// nodeLifecycleEvent wraps a NodeLifecycleUpdate in a MistTrigger envelope and
// renders it to the kafka.AnalyticsEvent shape the ingest handler parses.
func nodeLifecycleEvent(t *testing.T, tenantID, clusterID string, ts time.Time, upd *ipcpb.NodeLifecycleUpdate) kafka.AnalyticsEvent {
	t.Helper()
	mt := &ipcpb.MistTrigger{
		ClusterId:      proto.String(clusterID),
		TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{NodeLifecycleUpdate: upd},
	}
	raw, err := protojson.Marshal(mt)
	if err != nil {
		t.Fatalf("marshal MistTrigger: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal proto json: %v", err)
	}
	return kafka.AnalyticsEvent{
		EventID:   "evt-node",
		EventType: "node_lifecycle_update",
		Timestamp: ts,
		TenantID:  tenantID,
		Data:      data,
	}
}

// TestProcessNodeLifecycle_MapsBothFacts pins the dual-write (node_state_current
// + node_metrics_samples) and the field derivations: CPU tenths→percent,
// operational-mode prefix strip (unspecified→normal), and the deliberate
// is_healthy representation split (uint8 in current state, bool in the metrics
// log).
func TestProcessNodeLifecycle_MapsBothFacts(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	ts := time.Unix(1_700_000_000, 0).UTC()
	upd := &ipcpb.NodeLifecycleUpdate{
		NodeId:          "node-1",
		CpuTenths:       355, // → 35.5%
		IsHealthy:       true,
		OperationalMode: ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED, // → "normal"
		ActiveStreams:   3,
	}
	event := nodeLifecycleEvent(t, "tenant-1", "cluster-1", ts, upd)

	if err := h.processNodeLifecycle(context.Background(), event); err != nil {
		t.Fatalf("processNodeLifecycle: %v", err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("expected 2 appended facts (state + metrics), got %d", len(batch.rows))
	}
	if !batch.sendCalled {
		t.Fatal("expected batches to be sent")
	}

	state := batch.rows[0] // node_state_current
	if state[0] != "tenant-1" || state[1] != "cluster-1" || state[2] != "node-1" {
		t.Errorf("state identity cols wrong: %v / %v / %v", state[0], state[1], state[2])
	}
	if state[3] != float32(35.5) {
		t.Errorf("cpu_percent = %v, want 35.5 (tenths/10)", state[3])
	}
	if state[12] != uint8(1) {
		t.Errorf("state is_healthy = %v, want uint8(1)", state[12])
	}
	if state[13] != "normal" {
		t.Errorf("operational_mode = %v, want \"normal\" (unspecified normalized)", state[13])
	}

	metrics := batch.rows[1] // node_metrics_samples
	if metrics[4] != float32(35.5) {
		t.Errorf("metrics cpu_usage = %v, want 35.5", metrics[4])
	}
	// Deliberate representation split: the metrics log stores the raw bool.
	if metrics[17] != true {
		t.Errorf("metrics is_healthy = %v, want bool true (raw, not uint8)", metrics[17])
	}
}

func TestProcessNodeLifecycle_UnhealthyNode(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	event := nodeLifecycleEvent(t, "tenant-1", "cluster-1", time.Unix(1_700_000_000, 0).UTC(),
		&ipcpb.NodeLifecycleUpdate{NodeId: "node-2", IsHealthy: false})

	if err := h.processNodeLifecycle(context.Background(), event); err != nil {
		t.Fatalf("processNodeLifecycle: %v", err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("expected 2 facts, got %d", len(batch.rows))
	}
	if batch.rows[0][12] != uint8(0) {
		t.Errorf("state is_healthy = %v, want uint8(0) for unhealthy node", batch.rows[0][12])
	}
	if batch.rows[1][17] != false {
		t.Errorf("metrics is_healthy = %v, want bool false", batch.rows[1][17])
	}
}

func TestProcessNodeLifecycle_RejectsWrongPayload(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	// A MistTrigger with no NodeLifecycleUpdate payload must be rejected, not
	// silently written as an empty node fact.
	mt := &ipcpb.MistTrigger{ClusterId: proto.String("cluster-1")}
	raw, _ := protojson.Marshal(mt)
	var data map[string]any
	_ = json.Unmarshal(raw, &data)
	event := kafka.AnalyticsEvent{EventID: "evt", EventType: "node_lifecycle_update", Timestamp: time.Now(), TenantID: "tenant-1", Data: data}

	if err := h.processNodeLifecycle(context.Background(), event); err == nil {
		t.Fatal("expected error for missing NodeLifecycleUpdate payload")
	}
	if len(batch.rows) != 0 {
		t.Error("no fact should be written when the payload is wrong")
	}
}
