package federation

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
)

func TestPreparedVODReservesNodeBandwidth(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	f, reader := artifactFixture(t, []string{"node-00"})
	f.pair.Object.Authority.PlaybackId = "public-playback"
	authority := configuredAuthority(t, f)
	generation, _, err := reader.ResolveSourceGeneration(context.Background(), authority)
	if err != nil {
		t.Fatal(err)
	}
	f.query.SourceGeneration = generation
	runtime, request := mediaServeFixture(t, f)
	sm.SetNodeInfo(request.NodeId, "https://edge.test", true, nil, nil, "", "", nil)
	if _, err = runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil); err != nil {
		t.Fatal(err)
	}
	if n := sm.GetNodeState(request.NodeId); n == nil || n.AddBandwidth == 0 {
		t.Fatalf("VOD admission reserved no bandwidth: %+v", n)
	}
}

func TestPreparedIngestReservesNodeBandwidth(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	_, runtime, _, request := ingestRuntimeFixture(t, "rtmp")
	sm.SetNodeInfo(request.NodeId, "https://edge.test", true, nil, nil, "", "", nil)
	if _, err := runtime.Reconcile(context.Background(), request, PlacementReceipt{}, nil); err != nil {
		t.Fatal(err)
	}
	if n := sm.GetNodeState(request.NodeId); n == nil || n.IngressReservedBytesPerSec != capacity.DefaultIngestEstimate {
		t.Fatalf("ingest admission reserved no bandwidth: %+v", n)
	}
}

func TestOriginPullCapacityRefusalDoesNotBecomeReusable(t *testing.T) {
	registry := freshRegistry(t)
	budget := state.DefaultNodeCapacity()
	budget.IngressBudgetBytesPerSec = 100
	state.DefaultManager().SetNodeCapacity("local-edge", budget)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	for range 2 {
		if result, err := deps.ArrangeOriginPull(context.Background(), makeReq()); err == nil || result != nil {
			t.Fatalf("capacity refusal turned into a usable pull: %+v %v", result, err)
		}
	}
	if _, found, err := registry.CurrentInboundPull(context.Background(), "stream-1", "local-edge"); err != nil || found {
		t.Fatalf("refused pull left an accepted destination: found=%v err=%v", found, err)
	}
	if len(fed.calls) != 0 {
		t.Fatalf("notified the source before destination admission: %d", len(fed.calls))
	}
}
