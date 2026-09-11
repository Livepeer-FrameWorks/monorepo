package state

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestPlacementSnapshotMetricFreshness(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("node", "https://edge.example", true, nil, nil, "", "", nil)
	if got := sm.GetAllNodesSnapshot().Nodes[0].MetricsObservedAt; !got.IsZero() {
		t.Fatal("identity write manufactured a metric observation")
	}
	metricsForCPU(sm, "node", 20)
	observed := sm.GetAllNodesSnapshot().Nodes[0].MetricsObservedAt
	if observed.IsZero() {
		t.Fatal("metric report did not advance observation time")
	}
	sm.TouchNode("node", true)
	sm.SetNodeInfo("node", "https://new.example", true, nil, nil, "", "", nil)
	if got := sm.GetAllNodesSnapshot().Nodes[0].MetricsObservedAt; !got.Equal(observed) {
		t.Fatal("heartbeat or configuration refreshed capacity observations")
	}
	encoded, err := json.Marshal(sm.GetNodeState("node"))
	if err != nil {
		t.Fatal(err)
	}
	var replicated NodeState
	if err := json.Unmarshal(encoded, &replicated); err != nil {
		t.Fatal(err)
	}
	if !replicated.MetricsObservedAt.Equal(observed) {
		t.Fatal("replicated node lost original metric observation time")
	}
}

func TestPlacementSnapshotProtocolFreshnessIsIndependent(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	outputs := map[string]any{"RTMP": "rtmp://HOST:2935/play/$"}
	sm.SetNodeInfo("node", "https://edge.example", true, nil, nil, "", "", outputs)
	observed := sm.GetAllNodesSnapshot().Nodes[0].OutputsObservedAt
	if observed.IsZero() {
		t.Fatal("listener report has no observation time")
	}
	outputs["RTMP"] = "mutated"
	metricsForCPU(sm, "node", 20)
	sm.TouchNode("node", true)
	sm.SetNodeInfo("node", "https://edge.example", true, nil, nil, "", "not-json", nil)
	snapshot := sm.GetAllNodesSnapshot().Nodes[0]
	if !snapshot.OutputsObservedAt.Equal(observed) || snapshot.Outputs["RTMP"] != "rtmp://HOST:2935/play/$" {
		t.Fatal("metrics, heartbeat, malformed report or caller mutation refreshed protocols")
	}
	sm.SetNodeInfo("node", "https://new.example", true, nil, nil, "", "", nil)
	if !sm.GetNodeState("node").OutputsObservedAt.IsZero() {
		t.Fatal("address change retained listener evidence for the old host")
	}
	sm.SetNodeInfo("node", "https://edge.example", true, nil, nil, "", "{}", nil)
	if len(sm.GetNodeState("node").Outputs) != 0 {
		t.Fatal("empty report did not withdraw advertised listener")
	}
	encoded, err := json.Marshal(sm.GetNodeState("node"))
	if err != nil {
		t.Fatal(err)
	}
	var restored NodeState
	if decodeErr := json.Unmarshal(encoded, &restored); decodeErr != nil || restored.OutputsObservedAt.IsZero() || len(restored.Outputs) != 0 {
		t.Fatalf("protocol withdrawal was lost on serialization: %+v %v", restored, decodeErr)
	}
}

func TestPlacementSnapshotCoordinatePresence(t *testing.T) {
	zero, latitude, longitude, invalid, infinite := 0.0, 52.0, 5.0, math.NaN(), math.Inf(1)
	for _, tc := range []struct {
		name     string
		lat, lon *float64
		present  bool
	}{
		{"unknown", nil, nil, false},
		{"partial", &latitude, nil, false},
		{"explicit_zero", &zero, &zero, true},
		{"valid", &latitude, &longitude, true},
		{"nan", &invalid, &longitude, false},
		{"infinite", &latitude, &infinite, false},
		{"out_of_range", &latitude, func() *float64 { v := 181.0; return &v }(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewStreamStateManager()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeInfo("node", "https://edge.example", true, tc.lat, tc.lon, "", "", nil)
			if got := sm.GetAllNodesSnapshot().Nodes[0].HasCoordinates; got != tc.present {
				t.Fatalf("coordinate presence = %v, want %v", got, tc.present)
			}
		})
	}
}

func TestPlacementSnapshotPreservesStreamEvidenceAndProtocols(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("node", "https://edge.example", true, nil, nil, "", "", map[string]any{"HLS": map[string]any{"url": "/hls/$"}})
	observed := time.Now().Add(-time.Minute)
	sm.mu.Lock()
	sm.streamInstances["stream"] = map[string]*StreamInstanceState{"node": {TenantID: "tenant", BufferState: "DRY", Status: "live", LastUpdate: observed, Inputs: 0, Replicated: true}}
	sm.mu.Unlock()
	snapshot := sm.GetAllNodesSnapshot().Nodes[0]
	stream := snapshot.Streams["stream"]
	if stream.TenantID != "tenant" || stream.BufferState != "DRY" || stream.Status != "live" || !stream.ObservedAt.Equal(observed) || stream.Inputs != 0 || !stream.Replicated {
		t.Fatalf("lost replica evidence: %+v", stream)
	}
	legacy := sm.GetBalancerNodeSnapshots()[0].Streams["stream"]
	if legacy != stream {
		t.Fatalf("snapshot variants disagree on source evidence: %+v, %+v", legacy, stream)
	}
	snapshot.Outputs["HLS"].(map[string]any)["url"] = "corrupted"
	if sm.GetNodeState("node").Outputs["HLS"].(map[string]any)["url"] != "/hls/$" {
		t.Fatal("snapshot protocol map aliases live state")
	}
}
