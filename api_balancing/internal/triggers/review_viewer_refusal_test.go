package triggers

import (
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
	"time"
)

func TestReviewBandwidthRefusedUserNewAbandonsUnconfirmedViewer(t *testing.T) {
	resetStateTrigHandlers(t)
	p := minimalProcessorTrigHandlers(t)
	admitViewerPlacementForTest(t, p)
	const stream, tenant, node = "refused-stream", "tenant-refused", "refused-edge"
	p.streamCache.Set(streamContextCacheKey("live+"+stream), streamContext{TenantID: tenant, OriginClusterID: "cluster-origin", MaxViewers: 5, RequiresAuthKnown: true, RequiresAuth: false}, time.Minute)
	sm := state.DefaultManager()
	sm.SetNodeInfo(node, "", true, nil, nil, "", "", nil)
	token, tokenErr := mist.NewViewerSessionID()
	if tokenErr != nil {
		t.Fatal(tokenErr)
	}
	id, _ := sm.StartVirtualViewerByID(token, node, stream, "203.0.113.1")
	if id == "" {
		t.Fatal("PLAY_REWRITE fixture did not start viewer")
	}
	if err := sm.ReleaseNodeBandwidth(t.Context(), id, node); err != nil {
		t.Fatal(err)
	}
	sm.SetNodeCapacity(node, &ipcpb.NodeBandwidthCapacity{EgressBudgetBytesPerSec: 1, IngressBudgetBytesPerSec: 1})
	requestURL := "https://edge.example/hls/" + stream + "/index.m3u8?tkn=" + id
	response, abort, err := p.handleUserNew(&ipcpb.MistTrigger{NodeId: node, ClusterId: ptrTrigHandlers("test-cluster"), TenantId: ptrTrigHandlers(tenant), TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+" + stream, Connector: "HLS", RequestUrl: requestURL, SessionId: "mist-refused", Host: "203.0.113.1"}}})
	if err != nil || abort || response != "false" {
		t.Fatalf("fixture did not reach refused USER_NEW: %q %v %v", response, abort, err)
	}
	for _, v := range sm.GetVirtualViewersForNode(node) {
		if v.ID == id && v.State == state.VirtualViewerActive {
			t.Fatal("refused USER_NEW left PLAY_REWRITE viewer active")
		}
	}
	if count := state.DefaultTenantCapacity().CountViewers(tenant); count != 0 {
		t.Fatalf("refused viewer retained tenant capacity: %d", count)
	}
}
