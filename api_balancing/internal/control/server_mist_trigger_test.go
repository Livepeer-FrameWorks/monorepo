package control

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/sirupsen/logrus"
)

type captureMistTriggerProcessor struct {
	last *pb.MistTrigger
	err  error
}

func (c *captureMistTriggerProcessor) ProcessTrigger(_ string, _ []byte, _ string) (string, bool, error) {
	return "", false, nil
}

func (c *captureMistTriggerProcessor) ProcessTypedTrigger(trigger *pb.MistTrigger) (string, bool, error) {
	c.last = trigger
	return "", false, c.err
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

func TestProcessMistTrigger_DurableAckReportsProcessorError(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	mistTriggerProcessor = &captureMistTriggerProcessor{err: errors.New("decklog publish failed")}
	localClusterID = "cluster-local"
	stream := &captureStream{}

	trigger := &pb.MistTrigger{
		TriggerType: "USER_END",
		Blocking:    false,
		RequestId:   "req-failed",
		TriggerPayload: &pb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &pb.ViewerDisconnectTrigger{StreamName: "live+abc"},
		},
	}

	processMistTrigger(trigger, "node-1", stream, logging.Logger(logrus.New()))

	msg := stream.lastSent()
	if msg == nil {
		t.Fatal("expected durable ack")
	}
	ack := msg.GetMistTriggerAck()
	if ack == nil {
		t.Fatalf("expected MistTriggerAck, got %T", msg.GetPayload())
	}
	if ack.GetSuccess() {
		t.Fatal("expected negative ack")
	}
	if !ack.GetRetryable() {
		t.Fatal("expected processor error to be retryable")
	}
	if ack.GetRequestId() != "req-failed" {
		t.Fatalf("expected request id req-failed, got %q", ack.GetRequestId())
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
	sm.SetNodeConnectionInfo(context.Background(), "node-remote", "10.0.0.5", "", "cluster-remote", nil)

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

func TestProcessMistTrigger_NodeRegistryClusterOverridesProvidedClusterID(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	sm := state.ResetDefaultManagerForTests()
	sm.SetNodeConnectionInfo(context.Background(), "node-remote", "10.0.0.5", "", "cluster-registered", nil)

	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	providedCluster := "cluster-from-trigger"
	trigger := &pb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-4",
		ClusterId:   &providedCluster,
		TriggerPayload: &pb.MistTrigger_PushEnd{
			PushEnd: &pb.PushEndTrigger{StreamName: "live+abc"},
		},
	}

	processMistTrigger(trigger, "node-remote", nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != "cluster-registered" {
		t.Fatalf("expected registered node cluster to override provided cluster, got %q", capture.last.GetClusterId())
	}
}
