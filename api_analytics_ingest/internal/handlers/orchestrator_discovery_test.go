package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestProcessOrchestratorDiscoveryObserved_MapsSample pins the discovery-sample
// mapping: identity from the envelope, bool→uint8 for reachable/compatible/
// dialed, and the empty-geo_source→"unknown" default. With no vantage the
// per-vantage current-row upsert is skipped, so exactly one sample is written.
func TestProcessOrchestratorDiscoveryObserved_MapsSample(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	msg := &ipcpb.OrchestratorDiscoveryObserved{
		OrchAddr:   "0xorch",
		OrchUrl:    "https://orch.example",
		Reachable:  true,
		Compatible: true,
		// Vantage left nil → handler substitutes an empty geo (dialed=false).
	}
	event := orchestratorEvent(t, "tenant-1", time.Unix(1_700_000_000, 0).UTC(), msg, map[string]any{
		"gateway_id":     "gw-1",
		"gateway_region": "eu-west",
		"cluster_id":     "cluster-1",
	})

	if err := h.processOrchestratorDiscoveryObserved(context.Background(), event); err != nil {
		t.Fatalf("processOrchestratorDiscoveryObserved: %v", err)
	}
	if len(batch.rows) != 1 {
		t.Fatalf("expected exactly 1 discovery sample (no vantage → no upsert), got %d", len(batch.rows))
	}
	r := batch.rows[0]
	// cols: timestamp, tenant_id, gateway_id, gateway_region, orch_addr, orch_url,
	// resolved_ip, advertised_node_url, discovery_latency_ms, reachable, compatible,
	// score, dialed, failure_reason, failure_kind, lat, lon, country_code, geo_source
	if r[1] != "tenant-1" || r[2] != "gw-1" || r[4] != "0xorch" {
		t.Errorf("identity cols wrong: tenant=%v gateway=%v orch=%v", r[1], r[2], r[4])
	}
	if r[9] != uint8(1) || r[10] != uint8(1) {
		t.Errorf("reachable/compatible = %v/%v, want uint8(1)/uint8(1)", r[9], r[10])
	}
	if r[12] != uint8(0) {
		t.Errorf("dialed = %v, want uint8(0) (no vantage)", r[12])
	}
	if r[18] != "unknown" {
		t.Errorf("geo_source = %v, want \"unknown\" (empty normalized)", r[18])
	}
}

func TestProcessOrchestratorDiscoveryObserved_RejectsMissingIdentity(t *testing.T) {
	batch := &captureBatch{}
	h := &AnalyticsHandler{clickhouse: &captureClickhouse{batch: batch}, logger: logging.NewLoggerWithService("test")}

	// Missing cluster_id → requireOrchestratorIdentity fails, no sample written.
	event := orchestratorEvent(t, "tenant-1", time.Now(), &ipcpb.OrchestratorDiscoveryObserved{OrchAddr: "0xorch"}, map[string]any{
		"gateway_id": "gw-1",
	})

	if err := h.processOrchestratorDiscoveryObserved(context.Background(), event); err == nil {
		t.Fatal("expected error when cluster_id is missing")
	}
	if len(batch.rows) != 0 {
		t.Error("no sample should be written when identity is incomplete")
	}
}
