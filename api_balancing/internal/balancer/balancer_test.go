package balancer

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
)

func seedNode(sm *state.StreamStateManager, nodeID string, capEdge bool, roles []string) {
	sm.TouchNode(nodeID, true)
	sm.SetNodeInfo(nodeID, "https://"+nodeID+".example.com", true, nil, nil, "test", "", map[string]interface{}{})
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		MaxTranscodes        int
		CurrentTranscodes    int
	}{
		CPU:                  10,
		RAMMax:               1024,
		RAMCurrent:           128,
		UpSpeed:              0,
		DownSpeed:            0,
		BWLimit:              1000,
		CapIngest:            false,
		CapEdge:              capEdge,
		CapStorage:           false,
		CapProcessing:        false,
		Roles:                roles,
		StorageCapacityBytes: 0,
		StorageUsedBytes:     0,
		MaxTranscodes:        0,
		CurrentTranscodes:    0,
	})
}

func TestGetTopNodesWithScoresMissingCapabilities(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	seedNode(sm, "node-missing-edge", false, nil)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	ctx := context.WithValue(context.Background(), ctxkeys.KeyCapability, "edge")

	_, err := lb.GetTopNodesWithScores(ctx, "stream-one", 0, 0, map[string]int{}, "", 1, false)
	if err == nil {
		t.Fatal("expected error for missing capabilities")
	}
	if !strings.Contains(err.Error(), "no nodes match required capabilities") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetTopNodesWithScoresAmbiguousStreamErrors(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	seedNode(sm, "node-no-stream", true, nil)
	seedNode(sm, "node-no-inputs", true, nil)

	streamName := "stream-missing-or-no-inputs"
	sm.UpdateNodeStats(streamName, "node-no-inputs", 0, 0, 0, 0, false)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	_, err := lb.GetTopNodesWithScores(context.Background(), streamName, 0, 0, map[string]int{}, "", 1, true)
	if err == nil {
		t.Fatal("expected error for missing/no-inputs stream")
	}
	if !strings.Contains(err.Error(), "missing or no inputs") {
		t.Fatalf("unexpected error: %v", err)
	}
}
