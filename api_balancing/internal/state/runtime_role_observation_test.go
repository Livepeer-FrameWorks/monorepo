package state

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
)

func TestIsolatedRuntimeRoleOverridesStaleReplicationHints(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	const internal, node, tenant = "stream", "node", "tenant"
	for _, role := range []streamident.RuntimeRole{streamident.RuntimePublisher, streamident.RuntimeRelay} {
		runtime, err := streamident.BoundRuntimeName(internal, role, "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9")
		if err != nil {
			t.Fatal(err)
		}
		wantReplica := role == streamident.RuntimeRelay
		sm.BeginIsolatedRuntime(internal, node, tenant, runtime)
		if err := sm.UpdateStreamFromBuffer(runtime, internal, node, tenant, "FULL", ""); err != nil {
			t.Fatal(err)
		}
		sm.ObserveRuntimeNodeSnapshotStream(internal, node, runtime, 3, 1, 100, 200, !wantReplica)
		got := sm.GetStreamInstances(internal)[node]
		if got.Replicated != wantReplica || got.RuntimeName != runtime || !got.Playable {
			t.Errorf("bound role %v was overwritten by a contradictory lifecycle hint: %+v", role, got)
		}
	}
}
