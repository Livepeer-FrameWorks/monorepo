package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// nullIfEmpty maps "" to a nil *string so empty optional columns persist as SQL
// NULL rather than an empty-string literal. The distinction is load-bearing for
// COALESCE/IS NULL filters downstream, so a non-empty value must round-trip by
// pointer identity.
func TestNullIfEmpty(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("nullIfEmpty(\"\") = %v, want nil", got)
	}
	s := "value"
	got := nullIfEmpty(s)
	if got == nil || *got != "value" {
		t.Fatalf("nullIfEmpty(%q) = %v, want pointer to %q", s, got, s)
	}
}

// resolvePlaybackAuthInvalidationNames has two regimes. When the caller supplies
// an explicit name list it is taken verbatim — trimmed, de-duplicated, and
// order-preserved — without consulting the load balancer or DB. We exercise that
// pure regime on a zero-value receiver: it must return before touching s.lb /
// s.db, so blanks drop, duplicates collapse to first occurrence, and the input
// order is retained.
func TestResolvePlaybackAuthInvalidationNames_RequestedIsPureAndDeduped(t *testing.T) {
	s := &FoghornGRPCServer{} // nil lb/db: requested-path must not dereference them
	got := s.resolvePlaybackAuthInvalidationNames(context.Background(), "tenant-x", []string{
		"live+a",
		"  live+b  ", // trimmed
		"live+a",     // duplicate of first → dropped
		"   ",        // blank → dropped
		"live+c",
	})
	want := []string{"live+a", "live+b", "live+c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// clipProcessingPreferredNode answers "can this exact node transcode the clip
// locally?" and returns "" (defer to normal scheduling) unless every condition
// holds: the node exists, is healthy, advertises the processing cap, and has
// free capacity in the video_transcode class. We seed the shared state manager
// with a uniquely-named node so the assertions don't collide with other tests.
func TestClipProcessingPreferredNode(t *testing.T) {
	if got := clipProcessingPreferredNode(""); got != "" {
		t.Errorf("empty nodeID = %q, want \"\"", got)
	}
	if got := clipProcessingPreferredNode("node-never-registered-xyz"); got != "" {
		t.Errorf("unknown node = %q, want \"\"", got)
	}

	sm := state.DefaultManager()
	const node = "clip-pref-happy-node"
	seedProcessingNode(sm, node, true, map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 4, Used: 0},
	})
	if got := clipProcessingPreferredNode(node); got != node {
		t.Errorf("healthy processing node with free transcode capacity = %q, want %q", got, node)
	}

	// Saturated transcode class (Used==Total) → CanRunClass false → defer.
	const full = "clip-pref-full-node"
	seedProcessingNode(sm, full, true, map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 2, Used: 2},
	})
	if got := clipProcessingPreferredNode(full); got != "" {
		t.Errorf("saturated transcode node = %q, want \"\" (defer)", got)
	}

	// Healthy + free capacity but no processing cap → defer.
	const noCap = "clip-pref-nocap-node"
	seedProcessingNodeRaw(sm, noCap, true, false, map[string]state.ClassCapacity{
		mist.ProcessingClassVideoTranscode: {Total: 4, Used: 0},
	})
	if got := clipProcessingPreferredNode(noCap); got != "" {
		t.Errorf("node without processing cap = %q, want \"\" (defer)", got)
	}
}

func seedProcessingNode(sm *state.StreamStateManager, nodeID string, healthy bool, classes map[string]state.ClassCapacity) {
	seedProcessingNodeRaw(sm, nodeID, healthy, true, classes)
}

func seedProcessingNodeRaw(sm *state.StreamStateManager, nodeID string, healthy, capProcessing bool, classes map[string]state.ClassCapacity) {
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
		ProcessingClasses    map[string]state.ClassCapacity
	}{
		CapProcessing:     capProcessing,
		ProcessingClasses: classes,
	})
	sm.TouchNode(nodeID, healthy)
}
