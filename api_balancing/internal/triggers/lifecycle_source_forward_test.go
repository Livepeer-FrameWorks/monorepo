package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// stream_state_current keeps one row per stream, so only the source's live
// report may reach Periscope. A relay copy of the same stream on another edge
// reports its own node and a replicated input; forwarding it overwrote the
// stream's node and track details.
func TestLiveLifecycleForwardsOnlyTheSource(t *testing.T) {
	reg := installRegistryForTest(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client
	const tenant = "tenant-src"

	streamIDs := map[string]string{
		"owned":   "11111111-1111-4111-8111-000000000001",
		"carried": "11111111-1111-4111-8111-000000000002",
		"unknown": "11111111-1111-4111-8111-000000000003",
	}
	send := func(internal, node string, replicated bool) {
		t.Helper()
		p.streamCache.Set(tenant+":"+internal, streamContext{TenantID: tenant, StreamID: streamIDs[internal]}, time.Minute)
		tenantID, inputs := tenant, uint32(1)
		if _, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
			TriggerType: "STREAM_LIFECYCLE_UPDATE",
			NodeId:      node,
			TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
				TenantId: &tenantID, NodeId: node, InternalName: internal, Status: "live", TotalInputs: &inputs, Replicated: &replicated,
			}},
		}); err != nil {
			t.Fatalf("handleStreamLifecycleUpdate(%s, %s): %v", internal, node, err)
		}
	}
	forwardedFrom := func(internal string) []string {
		var nodes []string
		for _, trigger := range capture.received() {
			if update := trigger.GetStreamLifecycleUpdate(); update != nil && update.GetInternalName() == internal {
				nodes = append(nodes, trigger.GetNodeId())
			}
		}
		return nodes
	}

	// Recorded owner in this cell: its report is the stream's; a local relay's is not.
	const owned = "owned"
	projectSourceForTest(t, reg, owned, "edge-origin", 0, "", "gen-1", 1)
	send(owned, "edge-origin", false)
	send(owned, "edge-relay", true)
	if got := forwardedFrom(owned); len(got) != 1 || got[0] != "edge-origin" {
		t.Fatalf("owned stream forwarded from %v, want only edge-origin", got)
	}

	// Carrier cell: no owner, the destination of a recorded inbound pull. Its
	// first report arrives before Mist tags the pulled stream replicated.
	const carried = "carried"
	markReplicatingForTest(t, reg, carried, "peer-cell", "dtsc://origin-edge:4200", "edge-carrier", "https://carrier.example/view", "origin-edge")
	send(carried, "edge-carrier", false)
	send(carried, "edge-carrier", true)
	if got := forwardedFrom(carried); len(got) != 0 {
		t.Fatalf("relay destination forwarded %d report(s) as the stream's state", len(got))
	}

	// Nothing recorded (e.g. a restarted replica before ownership is rebuilt):
	// Mist's replicated tag separates a relay from an ingest.
	const unknown = "unknown"
	send(unknown, "edge-ingest", false)
	send(unknown, "edge-pulled", true)
	if got := forwardedFrom(unknown); len(got) != 1 || got[0] != "edge-ingest" {
		t.Fatalf("ownerless stream forwarded from %v, want only the non-replicated ingest", got)
	}

	// Load-balancer state still learns about every copy.
	if _, ok := sm.GetStreamInstances(owned)["edge-relay"]; !ok {
		t.Fatal("relay copy must still update routing state")
	}
}
