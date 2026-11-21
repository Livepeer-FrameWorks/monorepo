package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const nodeLifecycleTestTenantID = "11111111-1111-4111-8111-111111111111"

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
// is_healthy representation as the UInt8 required by both ClickHouse tables.
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
	event := nodeLifecycleEvent(t, nodeLifecycleTestTenantID, "cluster-1", ts, upd)

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
	if state[0] != uuid.MustParse(nodeLifecycleTestTenantID) || state[1] != "cluster-1" || state[2] != "node-1" {
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
	if metrics[17] != uint8(1) {
		t.Errorf("metrics is_healthy = %v, want uint8(1)", metrics[17])
	}
}

func TestProcessNodeLifecycle_UnhealthyNode(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	event := nodeLifecycleEvent(t, nodeLifecycleTestTenantID, "cluster-1", time.Unix(1_700_000_000, 0).UTC(),
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
	if batch.rows[1][17] != uint8(0) {
		t.Errorf("metrics is_healthy = %v, want uint8(0)", batch.rows[1][17])
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

func TestProcessNodeLifecycleWritesBudgetAndProvenance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		budget     uint64
		source     ipcpb.BandwidthBudgetSource
		wantBudget uint64
		wantSource string
	}{
		{"default", 0, ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_UNSPECIFIED, 125000000, "DEFAULT"},
		{"operator", 250000000, ipcpb.BandwidthBudgetSource_BANDWIDTH_BUDGET_SOURCE_OPERATOR, 250000000, "OPERATOR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch := &captureBatch{}
			h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}
			update := &ipcpb.NodeLifecycleUpdate{NodeId: "node", EgressBudgetBytesPerSec: tc.budget, BudgetSource: tc.source, ObservedSource: ipcpb.ObservedSource_OBSERVED_SOURCE_NIC, ObservedValid: proto.Bool(true), IsHealthy: true}
			if err := h.processNodeLifecycle(t.Context(), nodeLifecycleEvent(t, nodeLifecycleTestTenantID, "cluster", time.Now(), update)); err != nil {
				t.Fatal(err)
			}
			row := batch.rows[0]
			if len(row) != 22 {
				t.Fatalf("node fact has %d columns, want budget and source appended (21)", len(row))
			}
			var metadata map[string]any
			if err := json.Unmarshal(row[17].([]byte), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata["observed_source"] != "NIC" || metadata["observed_valid"] != true {
				t.Errorf("observation provenance missing: %v", metadata)
			}
			if row[19] != tc.wantBudget || row[20] != tc.wantSource {
				t.Errorf("budget/source=%v/%v, want %d/%s", row[19], row[20], tc.wantBudget, tc.wantSource)
			}
		})
	}
}

func TestProcessNodeLifecycleUsesAudienceWithLegacyFallback(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("audience-test")}
	update := &ipcpb.NodeLifecycleUpdate{NodeId: "node", Streams: map[string]*ipcpb.StreamData{
		"audience":      {Total: 3, AudienceViewers: proto.Uint32(2)},
		"internal-only": {Total: 50, AudienceViewers: proto.Uint32(0)},
		"legacy":        {Total: 4},
	}}
	if err := h.processNodeLifecycle(t.Context(), nodeLifecycleEvent(t, nodeLifecycleTestTenantID, "serving-cluster", time.Now(), update)); err != nil {
		t.Fatal(err)
	}
	row := batch.rows[0]
	if len(row) != 22 {
		t.Fatalf("node row has %d columns, want audience at column 22", len(row))
	}
	if row[21] != uint32(6) {
		t.Fatalf("node viewers=%v, want audience 2+0 and legacy 4", row[21])
	}
}

func TestProcessLegacyNodeMemoryIsStoredAsBytes(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}
	update := &ipcpb.NodeLifecycleUpdate{NodeId: "legacy", RamMax: 4 * 1024 * 1024, RamCurrent: 2 * 1024 * 1024, ShmTotalBytes: 1024, ShmUsedBytes: 512}
	if err := h.processNodeLifecycle(t.Context(), nodeLifecycleEvent(t, nodeLifecycleTestTenantID, "cluster", time.Now(), update)); err != nil {
		t.Fatal(err)
	}
	if len(batch.rows) != 2 {
		t.Fatalf("state and historical facts missing: %v", batch.rows)
	}
	state, samples := batch.rows[0], batch.rows[1]
	if state[4] != uint64(2*1024*1024*1024) || state[5] != uint64(4*1024*1024*1024) || samples[5] != uint64(4*1024*1024*1024) || samples[6] != uint64(2*1024*1024*1024) {
		t.Fatalf("legacy KiB counters were labelled bytes: state=%v samples=%v", state[4:6], samples[5:7])
	}
}

func TestByteMarkedNodeMemoryIsNotConvertedTwice(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}
	update := &ipcpb.NodeLifecycleUpdate{NodeId: "current", RamMax: 4 * 1024 * 1024 * 1024, RamCurrent: 2 * 1024 * 1024 * 1024, MemoryMetricsAreBytes: true}
	if err := h.processNodeLifecycle(t.Context(), nodeLifecycleEvent(t, nodeLifecycleTestTenantID, "cluster", time.Now(), update)); err != nil {
		t.Fatal(err)
	}
	if len(batch.rows) != 2 || batch.rows[1][5] != update.GetRamMax() || batch.rows[1][6] != update.GetRamCurrent() || batch.rows[1][26] != uint16(1) {
		t.Fatalf("byte marker was lost or converted twice: %v", batch.rows)
	}
}
