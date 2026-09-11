package decklog

import (
	"context"
	"strings"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestSendViewerTriggerRedactsBeforeRPCWithoutChangingAdmission(t *testing.T) {
	fake := newCapturingClient()
	client := newTestClient(fake)
	original := &ipcpb.MistTrigger{TriggerType: "USER_NEW", TenantId: proto.String("tenant"), RequestId: "request",
		TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{
			ViewerToken: "private-viewer-token", RequestUrl: "https://edge/live?jwt=private-viewer-token&fwsid=attach_01", SessionId: "session",
		}}}
	eventID := ""
	for range 2 {
		if err := client.SendTriggerContext(context.Background(), original); err != nil {
			t.Fatal(err)
		}
		encoded, err := proto.Marshal(fake.lastTrigger)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private-viewer-token") {
			t.Fatal("viewer token reached the telemetry RPC")
		}
		if fake.lastTrigger.GetViewerConnect().GetRequestUrl() != "https://edge/live?fwsid=attach_01" || fake.lastTrigger.GetViewerConnect().GetSessionId() != "session" {
			t.Fatal("telemetry correlation lost")
		}
		if original.GetViewerConnect().GetViewerToken() != "private-viewer-token" || !strings.Contains(original.GetViewerConnect().GetRequestUrl(), "jwt=") {
			t.Fatal("original admission credentials removed")
		}
		if original.GetEventId() == "" || original.GetEventId() != fake.lastTrigger.GetEventId() {
			t.Fatal("telemetry retry lost its stamped event identity")
		}
		if eventID != "" && original.GetEventId() != eventID {
			t.Fatal("telemetry retry generated a different event identity")
		}
		eventID = original.GetEventId()
	}
}
