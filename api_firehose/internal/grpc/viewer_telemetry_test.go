package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestViewerCredentialsNeverReachKafka(t *testing.T) {
	const canary = "viewer-canary-8b7a-secret"
	const raw = "https://viewer:" + canary + "@edge.test/hls/live?JWT=" + canary + "&future_signature=" + canary + "&fwsid=attach_01#" + canary
	for _, kind := range []string{"USER_NEW", "PLAY_REWRITE", "mismatched-label"} {
		t.Run(kind, func(t *testing.T) {
			producer := &fakeProducer{}
			server := newTestServer(producer)
			server.rawTriggersTopic = "raw-fixture"
			trigger := &ipcpb.MistTrigger{TriggerType: kind, NodeId: "edge-us", RequestId: "request", EventId: "01900000-0000-7000-8000-000000000001",
				TenantId: proto.String("2f64c7d0-8c66-4b3b-88c4-421f8a3027f2"), ClusterId: proto.String("serving-us"), OriginClusterId: proto.String("origin-eu"), ControlCellId: proto.String("cell-us")}
			payloadKey := "viewer_connect"
			if kind == "PLAY_REWRITE" {
				trigger.TriggerPayload = &ipcpb.MistTrigger_PlayRewrite{PlayRewrite: &ipcpb.ViewerResolveTrigger{RequestedStream: "public", RequestUrl: raw, OutputType: "HLS"}}
				payloadKey = "play_rewrite"
			} else {
				trigger.TriggerPayload = &ipcpb.MistTrigger_ViewerConnect{ViewerConnect: &ipcpb.ViewerConnectTrigger{StreamName: "live+stream", ViewerToken: canary, RequestUrl: raw, SessionId: "session", Connector: "HLS"}}
			}
			before := proto.CloneOf(trigger)
			if _, err := server.SendEvent(context.Background(), trigger); err != nil {
				t.Fatal(err)
			}
			if !proto.Equal(trigger, before) {
				t.Fatal("Decklog mutated the admission payload")
			}
			if len(producer.publishCalls) != 1 || len(producer.produceCalls) != 0 {
				t.Fatal("unexpected typed/raw event publication")
			}
			event := producer.publishCalls[0]
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encoded, []byte(canary)) {
				t.Fatal("viewer credential reached a serialized Kafka event")
			}
			payload, ok := event.Data[payloadKey].(map[string]any)
			if !ok || payload["request_url"] != "https://edge.test/hls/live?fwsid=attach_01" {
				t.Fatal("sanitized URL or correlation is absent")
			}
			if _, retained := payload["viewer_token"]; retained {
				t.Fatal("viewer_token field retained")
			}
			if event.EventID != trigger.EventId || event.TenantID != trigger.GetTenantId() || event.SourceClusterID != "serving-us" || event.StreamOriginClusterID != "origin-eu" || event.Data["control_cell_id"] != "cell-us" {
				t.Fatal("redaction changed event or federation identity")
			}
		})
	}
}
