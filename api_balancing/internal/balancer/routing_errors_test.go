package balancer

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// seedMetricsNode registers a healthy, non-stale node with explicit raw metrics
// so a test can drive a single admission gate (e.g. UpSpeed >= BWLimit to
// exhaust bandwidth, or zeroed limits for an invalid host).
func seedMetricsNode(t *testing.T, sm *state.StreamStateManager, id string, ramMax, bwLimit, upSpeed float64) {
	t.Helper()
	sm.SetNodeInfo(id, id, true, nil, nil, "", "", nil)
	sm.UpdateNodeMetrics(id, struct {
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
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		RAMMax:  ramMax,
		BWLimit: bwLimit,
		UpSpeed: upSpeed,
	})
	sm.TouchNode(id, true)
}

// GetTopNodesWithScores collapses a set of per-node rejections into ONE
// actionable operator-facing error when every candidate failed for the same
// reason. Each case seeds exactly one rejection reason and pins the message —
// the whole point is that "no node" tells the operator *why*, not just "no node".
func TestGetTopNodesWithScores_SingleReasonErrors(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(t *testing.T, sm *state.StreamStateManager)
		stream  string
		wantMsg string
	}{
		{
			name: "all out of bandwidth",
			seed: func(t *testing.T, sm *state.StreamStateManager) {
				seedMetricsNode(t, sm, "bw-node", 1024, 1000, 2000) // used > limit → BWAvailable 0
			},
			wantMsg: "all suitable nodes are out of bandwidth",
		},
		{
			name: "metrics not ready",
			seed: func(t *testing.T, sm *state.StreamStateManager) {
				seedMetricsNode(t, sm, "invalid-node", 0, 0, 0) // no ram_max/bw_limit
			},
			wantMsg: "node metrics not ready (missing ram_max/bw_limit)",
		},
		{
			name: "all in maintenance",
			seed: func(t *testing.T, sm *state.StreamStateManager) {
				addTestNode(t, sm, "maint-node", "maint-node", 0, 0, true)
				if err := sm.SetNodeOperationalMode(context.Background(), "maint-node", state.NodeModeMaintenance, "test"); err != nil {
					t.Fatalf("set maintenance: %v", err)
				}
			},
			wantMsg: "all suitable nodes are in maintenance",
		},
		{
			name: "all draining",
			seed: func(t *testing.T, sm *state.StreamStateManager) {
				addTestNode(t, sm, "drain-node", "drain-node", 0, 0, true)
				if err := sm.SetNodeOperationalMode(context.Background(), "drain-node", state.NodeModeDraining, "test"); err != nil {
					t.Fatalf("set draining: %v", err)
				}
			},
			wantMsg: "all suitable nodes are draining",
		},
		{
			name: "stream not present on any node",
			seed: func(t *testing.T, sm *state.StreamStateManager) {
				addTestNode(t, sm, "edge-node", "edge-node", 0, 0, true)
			},
			stream:  "live+absent",
			wantMsg: `not present on any active node`,
		},
		{
			name: "stream present but no inputs",
			seed: func(t *testing.T, sm *state.StreamStateManager) {
				addTestNode(t, sm, "edge-node", "edge-node", 0, 0, true)
				sm.UpdateNodeStats("live+idle", "edge-node", 0, 0, 0, 0, false) // 0 inputs
			},
			stream:  "live+idle",
			wantMsg: `no active inputs on any node`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sm := setupTestManager(t)
			sm.SetWeights(500, 500, 1000, 1000, 0) // survivors would score > 0
			c.seed(t, sm)

			lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
			_, err := lb.GetTopNodesWithScores(context.Background(), c.stream, 0, 0, nil, "", 1, true)
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			if !strings.Contains(err.Error(), c.wantMsg) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), c.wantMsg)
			}
		})
	}
}

// maxNodes caps the returned candidate list to the requested count, and the
// list is score-sorted descending — callers (e.g. top-N edge selection) rely on
// result[0] being the best.
func TestGetTopNodesWithScores_MaxNodesTruncationAndSort(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(500, 0, 0, 1000, 0) // cpu keeps every node > 0; geo differentiates

	// Viewer sits at node-a; the others are progressively farther but still
	// score positively (cpu component keeps them in).
	addTestNode(t, sm, "node-a", "node-a", 52, 5, true)
	addTestNode(t, sm, "node-b", "node-b", 50, 5, true)
	addTestNode(t, sm, "node-c", "node-c", 48, 5, true)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	nodes, err := lb.GetTopNodesWithScores(context.Background(), "", 52, 5, nil, "", 2, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected maxNodes=2 truncation, got %d", len(nodes))
	}
	if nodes[0].Score < nodes[1].Score {
		t.Fatalf("results not sorted descending: %d < %d", nodes[0].Score, nodes[1].Score)
	}
	if nodes[0].Host != "node-a" {
		t.Fatalf("closest node should win, got %q", nodes[0].Host)
	}
}

// For an anonymous viewer request (no stream), a node whose IP equals the
// client's gets a 5x affinity boost — keep the viewer on the box they're already
// talking to. With two otherwise-equal nodes, the same-host one must win.
func TestGetTopNodesWithScores_ClientSameHostAffinityBoost(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 1000, 0, 0) // equal bw-only score for both nodes

	addTestNode(t, sm, "node-a", "node-a", 0, 0, true)
	addTestNode(t, sm, "node-b", "node-b", 0, 0, true)
	// node-a's IP matches the client IP below.
	sm.SetNodeConnectionInfo(context.Background(), "node-a", "10.0.0.5", "", "", nil)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	nodes, err := lb.GetTopNodesWithScores(context.Background(), "", 0, 0, nil, "10.0.0.5", 5, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nodes[0].Host != "node-a" {
		t.Fatalf("same-host node should win the affinity boost, got %q (scores: %+v)", nodes[0].Host, nodes)
	}
}

// During SOURCE selection, a node must never be handed its own stream as the
// pull origin: if the client IP equals a node's IP, that node is skipped. Here
// the only node holding the stream is the requester's own host, so source
// resolution must fail rather than loop the node back to itself — and the same
// setup WITHOUT the matching client IP resolves successfully (the contrast
// proves the skip is what changed the outcome).
func TestGetTopNodesWithScores_SameHostSourceSelectionSkip(t *testing.T) {
	sm := setupTestManager(t)
	sm.SetWeights(0, 0, 1000, 0, 0)

	addTestNode(t, sm, "node-a", "node-a", 0, 0, true)
	sm.SetNodeConnectionInfo(context.Background(), "node-a", "10.0.0.5", "", "", nil)
	sm.UpdateNodeStats("live+x", "node-a", 1, 1, 0, 0, false) // node-a holds the stream as source

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))

	// Control: no client IP → node-a is a valid source.
	if _, err := lb.GetTopNodesWithScores(context.Background(), "live+x", 0, 0, nil, "", 1, true); err != nil {
		t.Fatalf("control (no client IP) should resolve node-a as source, got error: %v", err)
	}

	// Same-host: client IP equals node-a's IP → node-a skipped → no source.
	if _, err := lb.GetTopNodesWithScores(context.Background(), "live+x", 0, 0, nil, "10.0.0.5", 1, true); err == nil {
		t.Fatal("expected source resolution to fail when the only origin is the requester's own host")
	}
}
