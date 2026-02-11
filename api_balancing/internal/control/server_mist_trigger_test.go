package control

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/sirupsen/logrus"
)

type captureMistTriggerProcessor struct {
	last *pb.MistTrigger
}

func (c *captureMistTriggerProcessor) ProcessTrigger(_ string, _ []byte, _ string) (string, bool, error) {
	return "", false, nil
}

func (c *captureMistTriggerProcessor) ProcessTypedTrigger(trigger *pb.MistTrigger) (string, bool, error) {
	c.last = trigger
	return "", false, nil
}

func TestProcessMistTrigger_PopulatesLocalClusterIDWhenMissing(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	// No node state registered → falls back to localClusterID
	sm := state.ResetDefaultManagerForTests()
	_ = sm

	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-1",
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{StreamName: "live+abc"},
		},
	}

	processMistTrigger(trigger, "node-1", nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != "cluster-local" {
		t.Fatalf("expected cluster_id to default to local cluster, got %q", capture.last.GetClusterId())
	}
}

func TestProcessMistTrigger_PrefersNodeRegistryCluster(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	sm := state.ResetDefaultManagerForTests()
	sm.SetNodeConnectionInfo("node-remote", "10.0.0.5", "", "cluster-remote", nil)

	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-3",
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{StreamName: "live+abc"},
		},
	}

	processMistTrigger(trigger, "node-remote", nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != "cluster-remote" {
		t.Fatalf("expected cluster_id from node registry %q, got %q", "cluster-remote", capture.last.GetClusterId())
	}
}

func TestProcessMistTrigger_DoesNotOverwriteProvidedClusterID(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	providedCluster := "cluster-from-helmsman"
	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-2",
		ClusterId:   &providedCluster,
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{StreamName: "live+abc"},
		},
	}

	processMistTrigger(trigger, "node-1", nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != providedCluster {
		t.Fatalf("expected cluster_id to remain %q, got %q", providedCluster, capture.last.GetClusterId())
	}
}
