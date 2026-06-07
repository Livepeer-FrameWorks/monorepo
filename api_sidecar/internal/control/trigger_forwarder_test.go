package control

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestTriggerForwarderLogFieldsIncludeTriggerContext(t *testing.T) {
	stringPtr := func(value string) *string { return &value }
	fields := TriggerSummaryFields(&ipcpb.MistTrigger{
		RequestId:   "source-1",
		TriggerType: "USER_END",
		TenantId:    stringPtr("tenant-1"),
		NodeId:      "edge-1",
		StreamId:    stringPtr("stream-1"),
		Timestamp:   1770000000000,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
				SessionId:  "session-1",
				StreamName: "live+stream-1",
			},
		},
	}, "source-1")

	for key, want := range map[string]any{
		"source_event_id": "source-1",
		"trigger_type":    "USER_END",
		"tenant_id":       "tenant-1",
		"node_id":         "edge-1",
		"stream_id":       "stream-1",
		"session_id":      "session-1",
		"stream_name":     "live+stream-1",
		"received_at_ms":  int64(1770000000000),
	} {
		if got := fields[key]; got != want {
			t.Fatalf("%s = %#v, want %#v; fields=%#v", key, got, want, fields)
		}
	}
	if _, ok := fields["wal_age_ms"]; !ok {
		t.Fatalf("missing wal_age_ms: %#v", fields)
	}
}
