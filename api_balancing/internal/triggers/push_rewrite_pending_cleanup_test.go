package triggers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/ingesterrors"
	"frameworks/api_balancing/internal/state"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestPushRewritePostMintSerializationFailureAbortsPendingSession(t *testing.T) {
	for _, stage := range []string{"decklog", "push_targets"} {
		t.Run(stage, func(t *testing.T) {
			installIngestSessionMintThenAbortMock(t)
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
			sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "demo-media", nil)
			previousRegistry := control.StreamRegistryInstance
			control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-local", time.Minute))
			t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })

			response := &commodorepb.ValidateStreamKeyResponse{
				Valid: true, UserId: "user-1", TenantId: "tenant-1", InternalName: "stream-a",
				StreamId: "stream-1", BillingModel: "postpaid",
			}
			if stage == "push_targets" {
				response.PushTargets = []*commodorepb.PushTargetInternal{{Id: "target-1", TargetUri: "rtmp://example/push"}}
			}
			commodoreClient, cleanup := setupCommodoreClient(t, response, nil)
			t.Cleanup(cleanup)

			p := newTestProcessor(t)
			p.commodoreClient = commodoreClient
			p.clusterID = "cluster-local"
			p.marshalAdmissionEffect = func(message proto.Message) ([]byte, error) {
				switch message.(type) {
				case *ipcpb.MistTrigger:
					if stage == "decklog" {
						return nil, fmt.Errorf("injected Decklog serialization failure")
					}
				case *ipcpb.ActivatePushTargets:
					if stage == "push_targets" {
						return nil, fmt.Errorf("injected push-target serialization failure")
					}
				}
				return proto.Marshal(message)
			}

			_, blocking, err := p.handlePushRewrite(&ipcpb.MistTrigger{
				NodeId: "edge-node-1",
				TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{
					Pid: 4242, TriggerUuid: "test-trigger-uuid", TriggerUnixMillis: 1,
					StreamName: "sk_test", PushUrl: "rtmp://example/live/sk_test",
				}},
			})
			if err == nil || !blocking {
				t.Fatalf("serialization failure = blocking:%v err:%v, want blocking denial", blocking, err)
			}
			var ingestErr *ingesterrors.IngestError
			if !errors.As(err, &ingestErr) || ingestErr.Code != ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL {
				t.Fatalf("serialization failure error = %T %v, want typed internal ingest error", err, err)
			}
		})
	}
}
