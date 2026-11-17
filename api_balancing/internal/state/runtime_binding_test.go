package state

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"testing"
)

func TestIsolatedRuntimeDoesNotInheritOrAcceptOldBufferReadiness(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	const internal, node, tenant = "stream", "node", "tenant"
	publisher, err := streamident.BoundRuntimeName(internal, streamident.RuntimePublisher, "84ed1d31-13d2-468d-b7b5-ef132d4e3ee9")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := streamident.BoundRuntimeName(internal, streamident.RuntimeRelay, "faed1d31-13d2-468d-b7b5-ef132d4e3ee9")
	if err != nil {
		t.Fatal(err)
	}
	if err := sm.UpdateStreamFromBuffer("live+stream", internal, node, tenant, "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.BeginIsolatedRuntime(internal, node, tenant, publisher)
	requireStarting := func() {
		t.Helper()
		inst := sm.GetStreamInstances(internal)[node]
		if inst.Playable || inst.Inputs != 0 || inst.Status == "live" {
			t.Fatalf("new runtime inherited old source evidence: %+v", inst)
		}
	}
	requireStarting()
	if err := sm.UpdateStreamFromBuffer("live+stream", internal, node, tenant, "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.ObserveRuntimeNodeSnapshotStream(internal, node, "live+stream", 9, 1, 0, 0, false)
	requireStarting()
	if err := sm.UpdateStreamFromBuffer(publisher, internal, node, tenant, "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.BeginIsolatedRuntime(internal, node, tenant, publisher)
	inst := sm.GetStreamInstances(internal)[node]
	if !inst.Playable || inst.Inputs != 1 || inst.Replicated {
		t.Fatalf("current publisher did not become playable or replay cleared it: %+v", inst)
	}
	sm.BeginIsolatedRuntime(internal, node, tenant, relay)
	requireStarting()
	if err := sm.UpdateStreamFromBuffer(relay, internal, node, tenant, "FULL", ""); err != nil {
		t.Fatal(err)
	}
	sm.SetOfflineForRuntime(internal, node, publisher)
	sm.ObserveRuntimeNodeSnapshotStream(internal, node, publisher, 9, 1, 0, 0, false)
	inst = sm.GetStreamInstances(internal)[node]
	if !inst.Playable || !inst.Replicated || inst.Inputs != 0 || inst.RuntimeName != relay {
		t.Fatalf("late publisher callback changed successor relay: %+v", inst)
	}
}
