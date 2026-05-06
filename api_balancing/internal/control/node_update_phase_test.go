package control

import "testing"

func TestUpdatePhaseRestoresRoutingOnlyForCordonedUpdates(t *testing.T) {
	t.Parallel()

	restorePhases := []string{"cordoning", "draining", "drained", "updating_restore", "warming_restore"}
	for _, phase := range restorePhases {
		if !updatePhaseRestoresRouting(phase) {
			t.Fatalf("updatePhaseRestoresRouting(%q) = false, want true", phase)
		}
	}
	for _, phase := range []string{"updating", "warming", "idle", "failed"} {
		if updatePhaseRestoresRouting(phase) {
			t.Fatalf("updatePhaseRestoresRouting(%q) = true, want false", phase)
		}
	}
}

func TestUpdatePhaseAcceptsRestoreApplyResults(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{"updating", "updating_restore", "warming", "warming_restore"} {
		if !updatePhaseAcceptsApplyResult(phase) {
			t.Fatalf("updatePhaseAcceptsApplyResult(%q) = false, want true", phase)
		}
	}
}
