package handlers

import (
	"context"
	"encoding/json"
	"maps"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// captureBatch records the values appended to a ClickHouse batch so a test can
// assert the event→fact column mapping (the existing apiBatchFakeBatch only
// counts calls).
type captureBatch struct {
	rows       [][]any
	sendCalled bool
}

func (b *captureBatch) Append(v ...any) error {
	row := make([]any, len(v))
	copy(row, v)
	b.rows = append(b.rows, row)
	return nil
}
func (b *captureBatch) Send() error { b.sendCalled = true; return nil }

type captureClickhouse struct{ batch *captureBatch }

func (c *captureClickhouse) PrepareBatch(_ context.Context, _ string) (clickhouseBatch, error) {
	return c.batch, nil
}
func (c *captureClickhouse) Query(_ context.Context, _ string, _ ...any) (clickhouseRows, error) {
	return &apiBatchFakeRows{}, nil
}
func (c *captureClickhouse) Exec(_ context.Context, _ string, _ ...any) error { return nil }

// orchestratorEvent builds a kafka.AnalyticsEvent whose Data carries the proto
// fields (protojson) plus the orchestrator identity keys that live alongside
// them in the envelope. parseProtobufData uses DiscardUnknown, so the identity
// keys coexist with the proto body.
func orchestratorEvent(t *testing.T, tenantID string, ts time.Time, msg proto.Message, identity map[string]any) kafka.AnalyticsEvent {
	t.Helper()
	raw, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal proto: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal proto json: %v", err)
	}
	maps.Copy(data, identity)
	return kafka.AnalyticsEvent{
		EventID:   "evt-1",
		EventType: "orchestrator_outcome",
		Timestamp: ts,
		TenantID:  tenantID,
		Data:      data,
	}
}

func fullIdentity() map[string]any {
	return map[string]any{
		"gateway_id":              "gw-1",
		"gateway_region":          "eu-west",
		"cluster_id":              "cluster-1",
		"cluster_owner_tenant_id": "owner-1",
	}
}

func TestProcessOrchestratorTranscodeOutcome_MapsRow(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	ts := time.Unix(1_700_000_000, 0).UTC()
	msg := &ipcpb.OrchestratorTranscodeOutcome{
		OrchAddr:  "0xorch",
		OrchUrl:   "https://orch.example",
		SessionId: "sess-1",
		Success:   true,
		OverallMs: 150,
		Pixels:    123456,
	}
	event := orchestratorEvent(t, "tenant-1", ts, msg, fullIdentity())

	if err := h.processOrchestratorTranscodeOutcome(context.Background(), event); err != nil {
		t.Fatalf("processOrchestratorTranscodeOutcome: %v", err)
	}
	if len(batch.rows) != 1 || !batch.sendCalled {
		t.Fatalf("expected 1 appended row + Send, got rows=%d send=%v", len(batch.rows), batch.sendCalled)
	}
	row := batch.rows[0]
	// Column order from the INSERT: timestamp, tenant_id, cluster_owner_tenant_id,
	// gateway_id, gateway_region, cluster_id, orch_addr, orch_url, resolved_ip,
	// session_id, manifest_id_hash, seq_no, success, ...
	if row[1] != "tenant-1" {
		t.Errorf("tenant_id col = %v, want tenant-1", row[1])
	}
	if row[2] != "owner-1" {
		t.Errorf("cluster_owner_tenant_id col = %v, want owner-1", row[2])
	}
	if row[3] != "gw-1" || row[5] != "cluster-1" {
		t.Errorf("identity cols wrong: gateway=%v cluster=%v", row[3], row[5])
	}
	if row[6] != "0xorch" {
		t.Errorf("orch_addr col = %v, want 0xorch", row[6])
	}
	if row[12] != uint8(1) {
		t.Errorf("success col = %v, want uint8(1) (boolToUInt8)", row[12])
	}
}

func TestProcessOrchestratorTranscodeOutcome_RejectsMissingClusterOwner(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	identity := fullIdentity()
	delete(identity, "cluster_owner_tenant_id")
	event := orchestratorEvent(t, "tenant-1", time.Now(), &ipcpb.OrchestratorTranscodeOutcome{OrchAddr: "0xorch"}, identity)

	if err := h.processOrchestratorTranscodeOutcome(context.Background(), event); err == nil {
		t.Fatal("expected error when cluster_owner_tenant_id is missing")
	}
	if len(batch.rows) != 0 {
		t.Error("no row should be appended when validation fails")
	}
}

func TestProcessOrchestratorTranscodeOutcome_RejectsMissingIdentity(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	identity := fullIdentity()
	delete(identity, "gateway_id") // requireOrchestratorIdentity needs gateway_id + cluster_id
	event := orchestratorEvent(t, "tenant-1", time.Now(), &ipcpb.OrchestratorTranscodeOutcome{OrchAddr: "0xorch"}, identity)

	if err := h.processOrchestratorTranscodeOutcome(context.Background(), event); err == nil {
		t.Fatal("expected error when gateway_id is missing")
	}
}

func TestProcessOrchestratorAIOutcome_MapsRow(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	ts := time.Unix(1_700_000_000, 0).UTC()
	msg := &ipcpb.OrchestratorAIOutcome{
		OrchAddr:  "0xorch",
		SessionId: "sess-ai",
		Pipeline:  "text-to-image",
		Model:     "sdxl",
		LatencyMs: 900,
		Success:   false,
	}
	event := orchestratorEvent(t, "tenant-1", ts, msg, fullIdentity())

	if err := h.processOrchestratorAIOutcome(context.Background(), event); err != nil {
		t.Fatalf("processOrchestratorAIOutcome: %v", err)
	}
	if len(batch.rows) != 1 || !batch.sendCalled {
		t.Fatalf("expected 1 appended row + Send, got rows=%d send=%v", len(batch.rows), batch.sendCalled)
	}
	row := batch.rows[0]
	// Columns: timestamp, tenant_id, cluster_owner_tenant_id, gateway_id,
	// gateway_region, cluster_id, orch_addr, orch_url, resolved_ip, session_id,
	// pipeline, model, latency_score, price_per_unit, latency_ms, success, ...
	if row[1] != "tenant-1" || row[2] != "owner-1" {
		t.Errorf("tenant/owner cols wrong: %v / %v", row[1], row[2])
	}
	if row[10] != "text-to-image" || row[11] != "sdxl" {
		t.Errorf("pipeline/model cols wrong: %v / %v", row[10], row[11])
	}
	if row[15] != uint8(0) {
		t.Errorf("success col = %v, want uint8(0) for failed AI job", row[15])
	}
}
