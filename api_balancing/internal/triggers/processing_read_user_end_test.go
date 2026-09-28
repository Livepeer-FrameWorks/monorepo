package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A processing job reads its source over HTTP, which Mist accounts as a viewer session. Its
// USER_NEW is admitted as the platform's own read, so its USER_END must not reach Periscope as a
// viewer session either; a real viewer's USER_END still does.
func TestProcessingReadUserEndStaysOutOfViewerAnalytics(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client
	const tenant, internal = "tenant-read", "stream-read"
	p.streamCache.Set(tenant+":"+internal, streamContext{TenantID: tenant, StreamID: "11111111-1111-4111-8111-000000000010"}, time.Minute)

	end := func(sessionID, token, connector string) {
		t.Helper()
		tenantID := tenant
		trigger := &ipcpb.MistTrigger{
			TriggerType: string(mist.TriggerUserEnd),
			NodeId:      "edge-1",
			TenantId:    &tenantID,
			TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				SessionId: sessionID, SessionIdentifier: &token, StreamName: "live+" + internal,
				Connector: connector, Host: "192.0.2.10", Duration: 30,
			}},
		}
		if _, _, err := p.handleUserEnd(trigger); err != nil {
			t.Fatalf("handleUserEnd(%s): %v", sessionID, err)
		}
	}
	end("mist-viewer", "viewer-token", "HLS")
	end("mist-read", mist.ProcessingReadSessionPrefix+"job-1", "MKV")

	sessions := map[string]bool{}
	for _, trigger := range capture.received() {
		if disconnect := trigger.GetViewerDisconnect(); disconnect != nil {
			sessions[disconnect.GetSessionId()] = true
		}
	}
	if !sessions["mist-viewer"] {
		t.Fatalf("the viewer's USER_END did not reach Periscope: %v", sessions)
	}
	if sessions["mist-read"] {
		t.Fatalf("the processing read's USER_END reached Periscope as a viewer session: %v", sessions)
	}
}
