package jobs

import (
	"database/sql"
	"testing"

	"frameworks/api_balancing/internal/state"
)

func preferred(nodeID string) *processingJob {
	return &processingJob{PreferredNode: sql.NullString{String: nodeID, Valid: nodeID != ""}}
}

// routeProcessingJob picks the edge node for a transcode. The invariants:
//   - a viable preferred (source) node wins outright, honoring locality;
//   - a named-but-unviable preferred node fails closed to
//     "preferred source node unavailable" rather than silently rebalancing
//     elsewhere (the caller wants the source node or nothing);
//   - otherwise it spreads load by picking the fewest in-flight transcodes,
//     treating MaxTranscodes==0 as unbounded capacity.
func TestRouteProcessingJob(t *testing.T) {
	// Leave a fresh manager behind so a leftover alive node can't leak into
	// other tests in this package (the dispatcher routes to alive nodes).
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	t.Run("no alive nodes", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		if id, reason := routeProcessingJob(nil); id != "" || reason != "no alive nodes" {
			t.Errorf("got (%q,%q), want (\"\",\"no alive nodes\")", id, reason)
		}
	})

	t.Run("viable preferred node wins", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		sm.TouchNode("source", true)
		setNodeProcessing(sm, "source", true, 4, 1)
		sm.TouchNode("other", true)
		setNodeProcessing(sm, "other", true, 4, 0) // emptier, but not preferred
		if id, reason := routeProcessingJob(preferred("source")); id != "source" || reason != "preferred_source_node" {
			t.Errorf("got (%q,%q), want (\"source\",\"preferred_source_node\")", id, reason)
		}
	})

	t.Run("preferred missing fails closed", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		sm.TouchNode("other", true) // keeps aliveIDs non-empty
		setNodeProcessing(sm, "other", true, 4, 0)
		if id, reason := routeProcessingJob(preferred("ghost")); id != "" || reason != "preferred source node unavailable" {
			t.Errorf("got (%q,%q), want (\"\",\"preferred source node unavailable\")", id, reason)
		}
	})

	t.Run("preferred present but not processing-capable fails closed", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		sm.TouchNode("source", true)
		setNodeProcessing(sm, "source", false, 4, 0) // alive but cannot process
		if id, reason := routeProcessingJob(preferred("source")); id != "" || reason != "preferred source node unavailable" {
			t.Errorf("got (%q,%q), want (\"\",\"preferred source node unavailable\")", id, reason)
		}
	})

	t.Run("no preference picks lowest transcode load", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		sm.TouchNode("busy", true)
		setNodeProcessing(sm, "busy", true, 8, 5)
		sm.TouchNode("idle", true)
		setNodeProcessing(sm, "idle", true, 8, 1) // fewest in-flight
		sm.TouchNode("mid", true)
		setNodeProcessing(sm, "mid", true, 8, 3)
		if id, reason := routeProcessingJob(nil); id != "idle" || reason != "lowest_transcode_load" {
			t.Errorf("got (%q,%q), want (\"idle\",\"lowest_transcode_load\")", id, reason)
		}
	})

	t.Run("MaxTranscodes 0 is unbounded and eligible", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		sm.TouchNode("unbounded", true)
		setNodeProcessing(sm, "unbounded", true, 0, 99) // 99 in-flight but no cap
		if id, reason := routeProcessingJob(nil); id != "unbounded" || reason != "lowest_transcode_load" {
			t.Errorf("got (%q,%q), want (\"unbounded\",\"lowest_transcode_load\")", id, reason)
		}
	})

	t.Run("all capable nodes full yields none available", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		sm.TouchNode("full", true)
		setNodeProcessing(sm, "full", true, 2, 2) // at capacity
		if id, reason := routeProcessingJob(nil); id != "" || reason != "no processing-capable nodes available" {
			t.Errorf("got (%q,%q), want (\"\",\"no processing-capable nodes available\")", id, reason)
		}
	})
}
