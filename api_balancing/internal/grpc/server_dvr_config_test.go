package grpc

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

// TestSourceNodeFromHint pins the freshness gate on a source-node hint: only a
// known, healthy, recently-heartbeating node is accepted; a blank hint, an
// unknown node, or a stale/never-seen node fails closed so DVR never pins to a
// node that can't serve.
func TestSourceNodeFromHint(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()

	t.Run("blank hint", func(t *testing.T) {
		if _, _, ok := sourceNodeFromHint("  "); ok {
			t.Fatal("blank hint must fail")
		}
	})

	t.Run("unknown node", func(t *testing.T) {
		if _, _, ok := sourceNodeFromHint("ghost"); ok {
			t.Fatal("unknown node must fail")
		}
	})

	t.Run("known but never heartbeated", func(t *testing.T) {
		// SetNodeInfo without TouchNode leaves LastHeartbeat zero -> stale.
		sm.SetNodeInfo("stale", "https://stale", true, nil, nil, "", "", nil)
		if _, _, ok := sourceNodeFromHint("stale"); ok {
			t.Fatal("never-heartbeated node must fail closed")
		}
	})

	t.Run("healthy fresh node accepted", func(t *testing.T) {
		sm.SetNodeInfo("good", "https://good", true, nil, nil, "", "", nil)
		sm.TouchNode("good", true)
		id, base, ok := sourceNodeFromHint("good")
		if !ok || id != "good" || base != "https://good" {
			t.Fatalf("healthy node = (%q, %q, %v), want (good, https://good, true)", id, base, ok)
		}
	})
}
