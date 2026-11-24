package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/domainevents"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestPublisherEventsUseAttestedConnector(t *testing.T) {
	for _, tc := range []struct {
		pushURL, connector, protocol string
		event                        publicv1.IngestProtocol
	}{
		{"rtmps://encoder.example/live/key", "RTMP", "rtmp", publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP},
		{"srt://encoder.example:8889", "RTMP", "rtmp", publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP},
		{"http://edge.example:8080/event-stream.mkv", "EBML", "mkv", publicv1.IngestProtocol_INGEST_PROTOCOL_MKV},
	} {
		t.Run(tc.pushURL, func(t *testing.T) {
			connected := &byteArgRecorder{}
			installIngestSessionMint(t, true, connected)
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeConnectionInfo(context.Background(), "edge-event", "edge-event:18090", "", "cluster-origin", nil)
			previous := control.StreamRegistryInstance
			control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-origin", time.Minute))
			t.Cleanup(func() { control.SetStreamRegistry(previous) })
			client, cleanup := setupCommodoreClient(t, &commodorepb.ValidateStreamKeyResponse{
				Valid: true, TenantId: "11111111-1111-4111-8111-111111111111", UserId: "user-1",
				StreamId: "22222222-2222-4222-8222-222222222222", InternalName: "event-stream", OriginClusterId: proto.String("cluster-origin"),
			}, nil)
			t.Cleanup(cleanup)
			processor := newTestProcessor(t)
			processor.commodoreClient, processor.clusterID = client, "cluster-origin"
			admitIngestPlacementForTest(t, processor)
			payload := &ipcpb.PushRewriteTrigger{
				Pid: 4242, TriggerUuid: "event-protocol", TriggerUnixMillis: 1, StreamName: "event-stream",
				Hostname: "203.0.113.1", PushUrl: tc.pushURL, ObservedConnector: tc.connector,
			}
			_, blocked, err := processor.handlePushRewrite(&ipcpb.MistTrigger{
				NodeId: "edge-event", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: payload},
			})
			if err != nil || blocked {
				t.Fatalf("attested %s publisher refused: blocked=%v, err=%v", tc.connector, blocked, err)
			}
			_, message, err := events.Decode("stream.connected", connected.bytes())
			if err != nil {
				t.Fatal(err)
			}
			if got := message.(*publicv1.StreamConnected).GetProtocol(); got != tc.event {
				t.Errorf("connected protocol=%v; want %v from the attested connector", got, tc.event)
			}
			if payload.GetProtocol() != tc.protocol {
				t.Errorf("Decklog protocol=%q; want %s", payload.GetProtocol(), tc.protocol)
			}
		})
	}
}

func TestUnmappedIngestConnectorKeepsAnalyticsName(t *testing.T) {
	for _, connector := range []string{"HTTP", "DTSC"} {
		protocol := ingestEventProtocol(connector)
		if protocol != map[string]string{"HTTP": "http", "DTSC": "dtsc"}[connector] {
			t.Errorf("analytics protocol=%q for %s", protocol, connector)
		}
		if got := domainevents.IngestProtocol(protocol); got != publicv1.IngestProtocol_INGEST_PROTOCOL_UNSPECIFIED {
			t.Errorf("unmapped connector %s became supported protocol %v", connector, got)
		}
	}
}
