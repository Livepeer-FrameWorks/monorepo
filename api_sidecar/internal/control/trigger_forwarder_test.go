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

// TriggerSummaryFields is the incident-response logging contract Foghorn relies
// on to correlate a durable trigger. Each payload variant carries a different
// set of stable fields; a regression that drops a switch arm silently loses
// that correlation, so every arm is asserted.
func TestTriggerSummaryFieldsPerPayloadVariant(t *testing.T) {
	t.Run("nil trigger keeps only request id", func(t *testing.T) {
		fields := TriggerSummaryFields(nil, "evt-nil")
		if fields["source_event_id"] != "evt-nil" {
			t.Fatalf("source_event_id = %#v", fields["source_event_id"])
		}
		if _, ok := fields["trigger_type"]; ok {
			t.Fatalf("nil trigger must not carry trigger fields: %#v", fields)
		}
	})

	cases := []struct {
		name     string
		trigger  *ipcpb.MistTrigger
		wantKeys map[string]any
	}{
		{
			name: "stream end",
			trigger: &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
				StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+s"},
			}},
			wantKeys: map[string]any{"stream_name": "live+s"},
		},
		{
			name: "push end",
			trigger: &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_PushEnd{
				PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+s", PushId: 42},
			}},
			wantKeys: map[string]any{"stream_name": "live+s", "push_id": int64(42)},
		},
		{
			name: "recording complete",
			trigger: &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_RecordingComplete{
				RecordingComplete: &ipcpb.RecordingCompleteTrigger{StreamName: "live+s"},
			}},
			wantKeys: map[string]any{"stream_name": "live+s"},
		},
		{
			name: "recording segment",
			trigger: &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_RecordingSegment{
				RecordingSegment: &ipcpb.RecordingSegmentTrigger{StreamName: "live+s", DurationMs: 6000},
			}},
			wantKeys: map[string]any{"stream_name": "live+s", "duration_ms": int64(6000)},
		},
		{
			name: "process billing",
			trigger: &ipcpb.MistTrigger{TriggerPayload: &ipcpb.MistTrigger_ProcessBilling{
				ProcessBilling: &ipcpb.ProcessBillingEvent{StreamName: "live+s", ProcessType: "Livepeer"},
			}},
			wantKeys: map[string]any{"stream_name": "live+s", "process_type": "Livepeer"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.trigger.RequestId = "evt-1"
			tc.trigger.TriggerType = "TEST"
			fields := TriggerSummaryFields(tc.trigger, "evt-1")
			for k, want := range tc.wantKeys {
				if got := fields[k]; got != want {
					t.Errorf("%s = %#v, want %#v; fields=%#v", k, got, want, fields)
				}
			}
		})
	}
}
