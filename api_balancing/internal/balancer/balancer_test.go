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

func setupTestManager(t *testing.T) *state.StreamStateManager {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	return sm
}

func addTestNode(t *testing.T, sm *state.StreamStateManager, nodeID, baseURL string, lat, lon float64, touch bool) {
	t.Helper()
	sm.SetNodeInfo(nodeID, baseURL, true, &lat, &lon, "", "", nil)
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
		CPU:        0,
		RAMMax:     1024,
		RAMCurrent: 0,
		UpSpeed:    0,
		BWLimit:    1024 * 1024,
	})
	if touch {
		sm.TouchNode(nodeID, true)
	}
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

func TestGetTopNodesWithScores_SkipsStaleNode(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 1000, 0, 0)

	addTestNode(t, sm, "node-active", "node-active", 0, 0, true)
	addTestNode(t, sm, "node-stale", "node-stale", 0, 0, false)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	nodes, err := lb.GetTopNodesWithScores(context.Background(), "", 0, 0, nil, "", 5, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node after filtering stale nodes, got %d", len(nodes))
	}
	if nodes[0].Host != "node-active" {
		t.Fatalf("expected node-active to be selected, got %s", nodes[0].Host)
	}
}

func TestGetBestNodeWithScore_PrefersCloserGeo(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 0, 1000, 0)

	addTestNode(t, sm, "node-close", "node-close", 0, 0, true)
	addTestNode(t, sm, "node-far", "node-far", 0, 180, true)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	best, _, _, _, _, err := lb.GetBestNodeWithScore(context.Background(), "", 0, 0, nil, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "node-close" {
		t.Fatalf("expected node-close to be selected, got %s", best)
	}
}
