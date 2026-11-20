package state

import (
	"testing"
	"time"
)

func TestReviewRound2ScopedPreparationSurvivesPrimaryCleanup(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "primary-present", true: "primary-cleaned"}[cleanup], func(t *testing.T) {
			sm := setupStateManager(t)
			configureTestNode(sm, "a")
			configureTestNode(sm, "b")
			sm.StartVirtualViewerByID("viewer", "a", "stream", "203.0.113.1")
			sm.StartVirtualViewerByID("viewer", "b", "stream", "203.0.113.1")
			if !sm.AttachVirtualViewerSession("viewer", "b", "stream", "203.0.113.1", "mist-b") {
				t.Fatal("fixture did not attach fallback")
			}
			if cleanup {
				sm.mu.Lock()
				v := sm.virtualViewerForNodeLocked("viewer", "a")
				v.State = VirtualViewerDisconnected
				v.DisconnectTime = time.Now().Add(-6 * time.Minute)
				sm.cleanupOldViewersLocked("a", 5*time.Minute)
				sm.mu.Unlock()
			}
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("scoped preparation panicked after primary cleanup: %v", p)
				}
			}()
			if allowed, err := sm.PrepareViewerBandwidth(t.Context(), "viewer", "b", "stream"); !allowed || err != nil {
				t.Fatalf("existing fallback preparation refused: %v %v", allowed, err)
			}
			if !sm.HasActiveVirtualViewerSession("mist-b", "b", "stream") {
				t.Fatal("preparation overwrote the active fallback viewer")
			}
			if got := len(sm.GetVirtualViewersForNode("b")); got != 1 {
				t.Fatalf("preparation duplicated viewer index: %d", got)
			}
		})
	}
}
