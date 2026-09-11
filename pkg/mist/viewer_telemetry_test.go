package mist

import (
	"strings"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestViewerTelemetryRequestURL(t *testing.T) {
	for _, test := range []struct{ raw, id, clean string }{
		{"", "", ""},
		{"https://user:secret@edge/hls/live/index.m3u8?jwt=secret&tkn=secret&fwsid=attach_01#secret", "attach_01", "https://edge/hls/live/index.m3u8?fwsid=attach_01"},
		{"https://edge/live?future_auth=secret&quality=high&fwcid=correlation", "", "https://edge/live"},
		{"wss://edge/webrtc/live?JWT=secret&FWSID=secret", "", "wss://edge/webrtc/live"},
		{"rtmp://user:secret@edge/live?key=secret", "", "rtmp://edge/live"},
		{"dtsc://edge/live?receipt=secret", "", "dtsc://edge/live"},
		{"/hls/live?jwt=secret&fwsid=valid-._01", "valid-._01", "/hls/live?fwsid=valid-._01"},
		{"https://edge/live?fwsid=contains%20space", "", "https://edge/live"},
		{"https://edge/live?fwsid=" + strings.Repeat("a", 65), "", "https://edge/live"},
		{"https://edge/live?fwsid=one&fwsid=two", "", "https://edge/live"},
		{"https://edge/live?fwsid=one&signature=%zz#secret", "", "https://edge/live"},
		{"https://edge/live?fwsid=one;secret", "", "https://edge/live"},
		{"https://edge/%zz?jwt=secret", "", ""},
		{"https://edge/live?", "", "https://edge/live"},
		{"data:text/plain,secret?jwt=secret", "", ""},
		{"javascript://edge/secret", "", ""},
	} {
		t.Run(test.raw, func(t *testing.T) {
			id, clean := ViewerTelemetryRequestURL(test.raw)
			if id != test.id || clean != test.clean {
				t.Fatalf("got (%q, %q), want (%q, %q)", id, clean, test.id, test.clean)
			}
			if againID, again := ViewerTelemetryRequestURL(clean); againID != id || again != clean {
				t.Fatal("telemetry sanitization is not idempotent")
			}
		})
	}
}

func TestSanitizeViewerTelemetryPreservesAdmissionAndIdentity(t *testing.T) {
	const raw = "https://edge/live?jwt=secret&fwsid=attach_01"
	triggers := []*ipcpb.MistTrigger{
		{TriggerPayload: &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+stream", ViewerToken: "secret", RequestUrl: raw, SessionId: "session", Connector: "HLS"}}},
		{TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{PlayRewrite: &ipcpb.ViewerResolveTrigger{RequestedStream: "public", RequestUrl: raw, OutputType: "HLS"}}},
		{TriggerPayload: &ipcpb.MistTrigger_ConnectionPlay{ConnectionPlay: &ipcpb.ConnectionPlayTrigger{StreamName: "live+stream", RequestUrl: raw, Connector: "DTSC"}}},
	}
	for _, original := range triggers {
		original.TriggerType = "UNTRUSTED_LABEL"
		original.NodeId, original.RequestId, original.EventId = "node", "request", "event"
		original.TenantId, original.ClusterId = proto.String("tenant"), proto.String("cluster")
		before := proto.CloneOf(original)
		clean := SanitizeViewerTelemetry(original)
		if clean == original || !proto.Equal(original, before) {
			t.Fatal("telemetry sanitization mutated the admission trigger")
		}
		encoded, err := proto.Marshal(clean)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "secret") || !strings.Contains(string(encoded), "fwsid=attach_01") {
			t.Fatal("credential retained or client correlation lost")
		}
		if clean.GetTenantId() != "tenant" || clean.GetClusterId() != "cluster" || clean.GetNodeId() != "node" || clean.GetRequestId() != "request" || clean.GetEventId() != "event" {
			t.Fatal("telemetry identity changed")
		}
		if !proto.Equal(clean, SanitizeViewerTelemetry(clean)) {
			t.Fatal("repeated sanitization changed the telemetry")
		}
	}
	if SanitizeViewerTelemetry(nil) != nil {
		t.Fatal("nil trigger changed")
	}
	unrelated := &ipcpb.MistTrigger{TriggerType: "STREAM_END"}
	if SanitizeViewerTelemetry(unrelated) != unrelated {
		t.Fatal("unrelated trigger cloned")
	}
}
