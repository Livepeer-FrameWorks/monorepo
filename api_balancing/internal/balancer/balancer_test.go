package balancer

import (
	"context"
	"testing"

	"github.com/sirupsen/logrus"

	"frameworks/api_balancing/internal/state"
)

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

func TestGetTopNodesWithScores_SkipsStaleNode(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 1000, 0, 0)

	addTestNode(t, sm, "node-active", "node-active", 0, 0, true)
	addTestNode(t, sm, "node-stale", "node-stale", 0, 0, false)

	lb := NewLoadBalancer(logrus.New())
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

	lb := NewLoadBalancer(logrus.New())
	best, _, _, _, _, err := lb.GetBestNodeWithScore(context.Background(), "", 0, 0, nil, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "node-close" {
		t.Fatalf("expected node-close to be selected, got %s", best)
	}
}
