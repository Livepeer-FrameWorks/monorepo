package balancer

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// seedCapNode registers a healthy, well-provisioned node with explicit
// capability flags and roles, so a test can drive the capability filter without
// tripping any other admission gate.
func seedCapNode(t *testing.T, sm *state.StreamStateManager, id string, capIngest, capEdge, capStorage, capProcessing bool, roles []string) {
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
		RAMMax:        1024,
		BWLimit:       1000,
		CapIngest:     capIngest,
		CapEdge:       capEdge,
		CapStorage:    capStorage,
		CapProcessing: capProcessing,
		Roles:         roles,
	})
	sm.TouchNode(id, true)
}

// GetTopNodesWithScores skips any node that does not satisfy the required
// capability carried on the context (a comma list). A capability matches either
// a Cap* flag (ingest/edge/storage/processing) or a literal entry in Roles
// (everything else). When every active node is filtered out for capability, the
// error names the requirement so the operator knows what's missing. This pins
// each capability arm and the all-requirements-must-match (AND) semantics.
func TestGetTopNodesWithScores_CapabilityFilter(t *testing.T) {
	cases := []struct {
		name       string
		capability string
		// admit seeds a node that SHOULD pass the filter.
		admit func(t *testing.T, sm *state.StreamStateManager)
		// reject seeds a node that should be filtered out.
		reject func(t *testing.T, sm *state.StreamStateManager)
	}{
		{
			name:       "ingest",
			capability: "ingest",
			admit: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", true, false, false, false, nil)
			},
			reject: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, false, false, nil)
			},
		},
		{
			name:       "storage",
			capability: "storage",
			admit: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, false, true, false, nil)
			},
			reject: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, false, false, nil)
			},
		},
		{
			name:       "processing",
			capability: "processing",
			admit: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, false, false, true, nil)
			},
			reject: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, false, false, nil)
			},
		},
		{
			name:       "custom role (default arm)",
			capability: "gpu",
			admit: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, false, false, []string{"gpu"})
			},
			reject: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, false, false, nil)
			},
		},
		{
			name:       "comma list requires all (AND)",
			capability: "edge,storage",
			admit: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, true, false, nil)
			},
			reject: func(t *testing.T, sm *state.StreamStateManager) {
				seedCapNode(t, sm, "n", false, true, false, false, nil)
			}, // edge only
		},
	}

	for _, c := range cases {
		t.Run(c.name+"/admitted", func(t *testing.T) {
			sm := setupTestManager(t)
			sm.SetWeights(0, 0, 1000, 0, 0)
			c.admit(t, sm)

			lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
			ctx := context.WithValue(context.Background(), ctxkeys.KeyCapability, c.capability)
			nodes, err := lb.GetTopNodesWithScores(ctx, "", 0, 0, nil, "", 5, false)
			if err != nil {
				t.Fatalf("capable node should be admitted, got error: %v", err)
			}
			if len(nodes) != 1 {
				t.Fatalf("expected 1 admitted node, got %d", len(nodes))
			}
		})

		t.Run(c.name+"/filtered", func(t *testing.T) {
			sm := setupTestManager(t)
			sm.SetWeights(0, 0, 1000, 0, 0)
			c.reject(t, sm)

			lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
			ctx := context.WithValue(context.Background(), ctxkeys.KeyCapability, c.capability)
			_, err := lb.GetTopNodesWithScores(ctx, "", 0, 0, nil, "", 5, false)
			if err == nil {
				t.Fatal("incapable node should be filtered out, got no error")
			}
			want := "no nodes match required capabilities (" + c.capability + ")"
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
			}
		})
	}
}

// With no nodes registered at all, both the candidate scan and its thin
// best-node wrapper must surface an error rather than panic or return an empty
// winner. (A fresh manager yields a non-nil snapshot with zero nodes.)
func TestBalancer_EmptyManagerErrors(t *testing.T) {
	setupTestManager(t) // zero nodes
	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))

	_, err := lb.GetTopNodesWithScores(context.Background(), "", 0, 0, nil, "", 1, false)
	if err == nil || !strings.Contains(err.Error(), "no nodes available in unified state") {
		t.Fatalf("GetTopNodesWithScores on empty manager: got %v, want 'no nodes available' error", err)
	}

	if _, err := lb.GetBestNode(context.Background(), "", 0, 0, nil); err == nil {
		t.Fatal("GetBestNode on empty manager should error")
	}
}
