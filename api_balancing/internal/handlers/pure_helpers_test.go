package handlers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// StateToProtoMode is the boundary translation from the internal node-mode enum
// to the protobuf enum sent to Helmsman. The default arm is a safety net: any
// mode that is neither draining nor maintenance must report NORMAL, never an
// unknown/zero value that an edge could misinterpret as "drain me".
func TestStateToProtoMode(t *testing.T) {
	cases := []struct {
		mode state.NodeOperationalMode
		want ipcpb.NodeOperationalMode
	}{
		{state.NodeModeDraining, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING},
		{state.NodeModeMaintenance, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE},
		{state.NodeModeNormal, ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL},
		// An unrecognised mode must still resolve to NORMAL via the default arm.
		{state.NodeOperationalMode("bogus"), ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL},
	}
	for _, c := range cases {
		if got := StateToProtoMode(c.mode); got != c.want {
			t.Errorf("StateToProtoMode(%v) = %v, want %v", c.mode, got, c.want)
		}
	}
}

// nodeClassUsed / nodeClassTotal read a node's per-class processing capacity for
// the debug page. A class the node never reported must read as 0 (not panic on
// the absent map key), and Total 0 conventionally means "unbounded".
func TestNodeClassUsedTotal(t *testing.T) {
	node := state.EnhancedBalancerNodeSnapshot{
		ProcessingClasses: map[string]state.ClassCapacity{
			"transcode": {Used: 3, Total: 8},
		},
	}
	if got := nodeClassUsed(node, "transcode"); got != 3 {
		t.Errorf("nodeClassUsed transcode = %d, want 3", got)
	}
	if got := nodeClassTotal(node, "transcode"); got != 8 {
		t.Errorf("nodeClassTotal transcode = %d, want 8", got)
	}
	// Absent class: both read 0, no panic.
	if got := nodeClassUsed(node, "ml"); got != 0 {
		t.Errorf("nodeClassUsed absent = %d, want 0", got)
	}
	if got := nodeClassTotal(node, "ml"); got != 0 {
		t.Errorf("nodeClassTotal absent = %d, want 0", got)
	}
	// Nil map (node never reported classes) must not panic either.
	empty := state.EnhancedBalancerNodeSnapshot{}
	if got := nodeClassUsed(empty, "transcode"); got != 0 {
		t.Errorf("nodeClassUsed nil-map = %d, want 0", got)
	}
}

// findStreamSourceNodeID resolves which node is the live source of a stream for
// the debug page. The authoritative answer is StreamState.NodeID when set; only
// when it is empty does it fall back to scanning per-node instances, and that
// scan must pick a genuine origin: Inputs>0 AND not replicated AND not offline,
// breaking ties on the freshest LastUpdate. This pins the eligibility filter so
// a replicated/offline/idle instance is never reported as the source.
func TestFindStreamSourceNodeID(t *testing.T) {
	t.Run("nil stream returns empty", func(t *testing.T) {
		if got := findStreamSourceNodeID(nil, nil); got != "" {
			t.Fatalf("got %q, want \"\"", got)
		}
	})

	t.Run("explicit NodeID short-circuits the instance scan", func(t *testing.T) {
		s := &state.StreamState{InternalName: "live+s1", NodeID: "origin-a"}
		// Instances exist but must be ignored when NodeID is authoritative.
		insts := map[string]map[string]state.StreamInstanceState{
			"live+s1": {"other": {Inputs: 1, LastUpdate: time.Now()}},
		}
		if got := findStreamSourceNodeID(s, insts); got != "origin-a" {
			t.Fatalf("got %q, want origin-a", got)
		}
	})

	t.Run("no NodeID and no instances returns empty", func(t *testing.T) {
		s := &state.StreamState{InternalName: "live+s1"}
		if got := findStreamSourceNodeID(s, nil); got != "" {
			t.Fatalf("got %q, want \"\"", got)
		}
	})

	t.Run("falls back to freshest eligible instance", func(t *testing.T) {
		now := time.Now()
		s := &state.StreamState{InternalName: "live+s1"}
		insts := map[string]map[string]state.StreamInstanceState{
			"live+s1": {
				// Excluded: replicated (pull mirror, not the origin).
				"replicated": {Inputs: 1, Replicated: true, LastUpdate: now},
				// Excluded: offline.
				"offline": {Inputs: 1, Status: "offline", LastUpdate: now},
				// Excluded: no inputs (idle edge).
				"idle": {Inputs: 0, LastUpdate: now},
				// Eligible, older.
				"origin-old": {Inputs: 1, LastUpdate: now.Add(-time.Minute)},
				// Eligible, freshest — should win.
				"origin-new": {Inputs: 2, LastUpdate: now},
			},
		}
		if got := findStreamSourceNodeID(s, insts); got != "origin-new" {
			t.Fatalf("got %q, want origin-new (freshest eligible)", got)
		}
	})

	t.Run("all instances ineligible returns empty", func(t *testing.T) {
		s := &state.StreamState{InternalName: "live+s1"}
		insts := map[string]map[string]state.StreamInstanceState{
			"live+s1": {
				"replicated": {Inputs: 1, Replicated: true, LastUpdate: time.Now()},
				"offline":    {Inputs: 1, Status: "offline", LastUpdate: time.Now()},
			},
		}
		if got := findStreamSourceNodeID(s, insts); got != "" {
			t.Fatalf("got %q, want \"\" (none eligible)", got)
		}
	})
}
