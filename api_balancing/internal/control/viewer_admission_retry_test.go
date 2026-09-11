package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestViewerAdmissionRetryReadsCurrentPolicy(t *testing.T) {
	for _, triggerType := range []string{"PLAY_REWRITE", "USER_NEW"} {
		t.Run(triggerType, func(t *testing.T) {
			previous := mistTriggerProcessor
			t.Cleanup(func() { mistTriggerProcessor = previous })
			resetBlockingTriggerReplayForTest(t)
			capture := &captureMistTriggerProcessor{response: "live+stream"}
			if triggerType == "USER_NEW" {
				capture.response = "true"
			}
			mistTriggerProcessor = capture
			stream := &captureStream{}
			t.Cleanup(SetupTestRegistry("node", stream))
			registry.conns["node"].canonicalID, registry.conns["node"].clusterID = "node", "cluster"
			session := NodeSession{CanonicalNodeID: "node", ClusterID: "cluster"}
			trigger := &ipcpb.MistTrigger{TriggerType: triggerType, Blocking: true, TriggerUuid: "same-attempt", RequestId: "first"}
			processMistTrigger(trigger, session, stream, logging.NewLogger())
			if response := stream.lastSent().GetMistTriggerResponse(); response.GetAction() != ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE {
				t.Fatal("initial admission failed")
			}
			capture.response = ""
			trigger.RequestId = "retry"
			processMistTrigger(trigger, session, stream, logging.NewLogger())
			response := stream.lastSent().GetMistTriggerResponse()
			if capture.calls != 2 || response.GetAction() != ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY || response.GetRequestId() != "retry" {
				t.Fatal("viewer retry replayed old permission instead of current policy")
			}
			blockingTriggerReplay.Lock()
			entries := len(blockingTriggerReplay.entries)
			blockingTriggerReplay.Unlock()
			if entries != 0 {
				t.Fatal("viewer admission retained a generic replay entry")
			}
		})
	}
}

func TestViewerAdmissionRejectsRetirementDuringDecision(t *testing.T) {
	for _, triggerType := range []string{"PLAY_REWRITE", "USER_NEW"} {
		t.Run(triggerType, func(t *testing.T) {
			previous := mistTriggerProcessor
			t.Cleanup(func() { mistTriggerProcessor = previous })
			stream := &captureStream{}
			t.Cleanup(SetupTestRegistry("node", stream))
			registry.conns["node"].canonicalID, registry.conns["node"].clusterID = "node", "cluster"
			mistTriggerProcessor = sourceAdmissionProcessorFunc(func(*ipcpb.MistTrigger) (string, bool, error) {
				registry.mu.Lock()
				delete(registry.conns, "node")
				registry.mu.Unlock()
				return "true", false, nil
			})
			processMistTrigger(&ipcpb.MistTrigger{TriggerType: triggerType, Blocking: true, RequestId: "retired"},
				NodeSession{CanonicalNodeID: "node", ClusterID: "cluster"}, stream, logging.NewLogger())
			if !stream.lastSent().GetMistTriggerResponse().GetAbort() {
				t.Fatal("retired node registration received viewer permission")
			}
		})
	}
}
