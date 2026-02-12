package balancer

import (
	"bytes"
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

	addTestNode(t, sm, "node-close", "node-close", 1, 1, true)
	addTestNode(t, sm, "node-far", "node-far", 1, 180, true)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	best, _, _, _, _, err := lb.GetBestNodeWithScore(context.Background(), "", 1, 1, nil, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best != "node-close" {
		t.Fatalf("expected node-close to be selected, got %s", best)
	}
}

func TestGetTopNodesWithScores_ClusterScope(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 1000, 0, 0)

	// Node owned by tenantA
	addTestNode(t, sm, "node-a", "node-a", 0, 0, true)
	sm.SetNodeConnectionInfo("node-a", "", "tenantA", "cluster-eu", nil)

	// Node owned by tenantB
	addTestNode(t, sm, "node-b", "node-b", 0, 0, true)
	sm.SetNodeConnectionInfo("node-b", "", "tenantB", "cluster-us", nil)

	// Shared infrastructure node (no tenant)
	addTestNode(t, sm, "node-shared", "node-shared", 0, 0, true)

	// Second cluster owned by tenantA (co-located on same Foghorn)
	addTestNode(t, sm, "node-a2", "node-a2", 0, 0, true)
	sm.SetNodeConnectionInfo("node-a2", "", "tenantA", "cluster-ap", nil)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))

	t.Run("no scope returns all nodes", func(t *testing.T) {
		nodes, err := lb.GetTopNodesWithScores(context.Background(), "", 0, 0, nil, "", 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(nodes) != 4 {
			t.Fatalf("expected 4 nodes, got %d", len(nodes))
		}
	})

	t.Run("tenantA scope returns tenantA + shared nodes", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyClusterScope, "tenantA")
		nodes, err := lb.GetTopNodesWithScores(ctx, "", 0, 0, nil, "", 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// node-a (tenantA), node-a2 (tenantA), node-shared (no tenant)
		if len(nodes) != 3 {
			t.Fatalf("expected 3 nodes for tenantA scope, got %d", len(nodes))
		}
		ids := map[string]bool{}
		for _, n := range nodes {
			ids[n.NodeID] = true
		}
		if ids["node-b"] {
			t.Fatal("tenantB node should be excluded from tenantA scope")
		}
	})

	t.Run("tenantB scope returns tenantB + shared nodes", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyClusterScope, "tenantB")
		nodes, err := lb.GetTopNodesWithScores(ctx, "", 0, 0, nil, "", 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// node-b (tenantB), node-shared (no tenant)
		if len(nodes) != 2 {
			t.Fatalf("expected 2 nodes for tenantB scope, got %d", len(nodes))
		}
		ids := map[string]bool{}
		for _, n := range nodes {
			ids[n.NodeID] = true
		}
		if ids["node-a"] || ids["node-a2"] {
			t.Fatal("tenantA nodes should be excluded from tenantB scope")
		}
	})

	t.Run("co-located clusters pool together", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyClusterScope, "tenantA")
		nodes, err := lb.GetTopNodesWithScores(ctx, "", 0, 0, nil, "", 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Both tenantA clusters (cluster-eu + cluster-ap) should be eligible
		ids := map[string]bool{}
		for _, n := range nodes {
			ids[n.NodeID] = true
		}
		if !ids["node-a"] || !ids["node-a2"] {
			t.Fatal("both tenantA clusters should be pooled together on the same Foghorn")
		}
	})

	t.Run("origin-pull context must scope to tenant", func(t *testing.T) {
		// Simulates the arrangeOriginPull context with tenant scope.
		// Before the fix, KeyClusterScope was missing and all nodes were returned.
		ctx := context.WithValue(context.Background(), ctxkeys.KeyClusterScope, "tenantB")
		nodes, err := lb.GetTopNodesWithScores(ctx, "", 0, 0, nil, "", 10, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, n := range nodes {
			if n.NodeID == "node-a" || n.NodeID == "node-a2" {
				t.Fatalf("tenantA node %q must not appear in tenantB-scoped origin-pull", n.NodeID)
			}
		}
	})
}

func TestHostToBinaryIPv4Mapped(t *testing.T) {
	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	bin := lb.hostToBinary("203.0.113.10")

	if bin[10] != 0xff || bin[11] != 0xff {
		t.Fatalf("expected IPv4-mapped marker bytes, got %x %x", bin[10], bin[11])
	}
	if !bytes.Equal(bin[12:16], []byte{203, 0, 113, 10}) {
		t.Fatalf("unexpected IPv4 bytes: %v", bin[12:16])
	}
}

func TestNodeLookupHelpers(t *testing.T) {
	sm := setupTestManager(t)
	addTestNode(t, sm, "node-1", "edge-1.example.com", 0, 0, true)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))

	nodes := lb.GetNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node in map, got %d", len(nodes))
	}
	if _, ok := nodes["edge-1.example.com"]; !ok {
		t.Fatalf("expected host key edge-1.example.com, got keys=%v", keys(nodes))
	}

	allNodes := lb.GetAllNodes()
	if len(allNodes) != 1 {
		t.Fatalf("expected 1 node in all nodes snapshot, got %d", len(allNodes))
	}

	baseURL, err := lb.GetNodeByID("node-1")
	if err != nil {
		t.Fatalf("GetNodeByID returned error: %v", err)
	}
	if baseURL != "edge-1.example.com" {
		t.Fatalf("expected base URL edge-1.example.com, got %q", baseURL)
	}

	if got := lb.GetNodeIDByHost("edge-1.example.com"); got != "node-1" {
		t.Fatalf("expected node id node-1, got %q", got)
	}
	if got := lb.GetNodeIDByHost("missing.example.com"); got != "" {
		t.Fatalf("expected empty node id for missing host, got %q", got)
	}

	if _, err := lb.GetNodeByID("node-missing"); err == nil {
		t.Fatal("expected error for missing node id")
	}
}

func TestGetBestNode(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 1000, 0, 0)

	addTestNode(t, sm, "node-1", "edge-1.example.com", 0, 0, true)
	addTestNode(t, sm, "node-2", "edge-2.example.com", 0, 0, true)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	host, err := lb.GetBestNode(context.Background(), "", 0, 0, nil)
	if err != nil {
		t.Fatalf("GetBestNode returned error: %v", err)
	}
	if host != "edge-1.example.com" && host != "edge-2.example.com" {
		t.Fatalf("unexpected host selected: %q", host)
	}
}

func TestScoreRemoteEdges(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(100, 100, 100, 100, 0)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	scored := lb.ScoreRemoteEdges([]RemoteEdgeCandidate{
		{
			ClusterID:   "remote-a",
			NodeID:      "node-a",
			BaseURL:     "edge-a.example.com",
			GeoLat:      10,
			GeoLon:      20,
			BWAvailable: remoteBWRefCapacity,
			CPUPercent:  0,
			RAMUsed:     0,
			RAMMax:      100,
		},
		{
			ClusterID:   "remote-b",
			NodeID:      "node-b",
			BaseURL:     "edge-b.example.com",
			GeoLat:      10,
			GeoLon:      20,
			BWAvailable: 1,
			CPUPercent:  100,
			RAMUsed:     100,
			RAMMax:      100,
		},
		{
			ClusterID:   "remote-c",
			NodeID:      "node-c",
			BaseURL:     "edge-c.example.com",
			GeoLat:      10,
			GeoLon:      20,
			BWAvailable: remoteBWRefCapacity,
			CPUPercent:  0,
			RAMUsed:     0,
			RAMMax:      0,
		},
	}, 10, 20)

	if len(scored) != 1 {
		t.Fatalf("expected only one viable remote edge, got %d", len(scored))
	}
	if scored[0].ClusterID != "remote-a" || scored[0].NodeID != "node-a" {
		t.Fatalf("unexpected scored node: %+v", scored[0])
	}
	if scored[0].Score == 0 {
		t.Fatal("expected positive remote score after penalty")
	}
}

func TestApplyAdjustment(t *testing.T) {
	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	tags := []string{"edge", "premium"}

	if got := lb.applyAdjustment(tags, "", 50); got != 0 {
		t.Fatalf("empty match should return 0, got %d", got)
	}
	if got := lb.applyAdjustment(tags, "edge", 50); got != 50 {
		t.Fatalf("single tag match should apply adjustment, got %d", got)
	}
	if got := lb.applyAdjustment(tags, "storage,premium", 30); got != 30 {
		t.Fatalf("comma match should apply adjustment, got %d", got)
	}
	if got := lb.applyAdjustment(tags, "-edge", 40); got != 0 {
		t.Fatalf("inverted match with present tag should not apply, got %d", got)
	}
	if got := lb.applyAdjustment(tags, "-storage", 40); got != 40 {
		t.Fatalf("inverted match with missing tag should apply, got %d", got)
	}
}

func TestWeightAndStreamAccessors(t *testing.T) {
	sm := setupTestManager(t)
	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))

	lb.SetWeights(11, 22, 33, 44, 55)
	gotWeights := lb.GetWeights()
	if gotWeights["cpu"] != 11 || gotWeights["ram"] != 22 || gotWeights["bw"] != 33 || gotWeights["geo"] != 44 || gotWeights["bonus"] != 55 {
		t.Fatalf("unexpected weights: %+v", gotWeights)
	}

	if err := sm.UpdateStreamFromBuffer("stream-a", "stream-a", "node-a", "tenant-a", "FULL", ""); err != nil {
		t.Fatalf("failed to seed stream-a: %v", err)
	}
	if err := sm.UpdateStreamFromBuffer("stream-b", "stream-b", "node-b", "tenant-b", "FULL", ""); err != nil {
		t.Fatalf("failed to seed stream-b: %v", err)
	}

	streams := lb.GetStreamsByTenant("tenant-a")
	if len(streams) != 1 || streams[0].InternalName != "stream-a" {
		t.Fatalf("unexpected streams for tenant-a: %+v", streams)
	}

	instances := lb.GetStreamInstances("stream-a")
	if len(instances) != 1 {
		t.Fatalf("expected one stream instance, got %d", len(instances))
	}
	if _, ok := instances["node-a"]; !ok {
		t.Fatalf("expected instance on node-a, got keys=%v", keys(instances))
	}
}

func keys[M ~map[K]V, K comparable, V any](m M) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
