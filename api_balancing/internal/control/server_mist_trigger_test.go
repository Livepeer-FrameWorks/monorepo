package control

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/sirupsen/logrus"
)

type captureMistTriggerProcessor struct {
	last *ipcpb.MistTrigger
	err  error
}

func (c *captureMistTriggerProcessor) ProcessTrigger(_ string, _ []byte, _ string) (string, bool, error) {
	return "", false, nil
}

func (c *captureMistTriggerProcessor) ProcessTypedTrigger(trigger *ipcpb.MistTrigger) (string, bool, error) {
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

	trigger := &ipcpb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-1",
		TriggerPayload: &ipcpb.MistTrigger_PushEnd{
			PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+abc"},
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

	trigger := &ipcpb.MistTrigger{
		TriggerType: "USER_END",
		Blocking:    false,
		RequestId:   "req-failed",
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{StreamName: "live+abc"},
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

func TestProcessMistTrigger_PushInputCloseGetsDurableAck(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	mistTriggerProcessor = &captureMistTriggerProcessor{}
	localClusterID = "cluster-local"
	stream := &captureStream{}

	trigger := &ipcpb.MistTrigger{
		TriggerType: "PUSH_INPUT_CLOSE",
		Blocking:    false,
		RequestId:   "req-push-input-close",
		TriggerPayload: &ipcpb.MistTrigger_PushInputClose{
			PushInputClose: &ipcpb.PushInputCloseTrigger{StreamName: "live+abc"},
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
	if !ack.GetSuccess() {
		t.Fatalf("expected positive ack, got code=%s retryable=%v err=%q", ack.GetErrorCode(), ack.GetRetryable(), ack.GetErrorMessage())
	}
	if ack.GetRequestId() != "req-push-input-close" {
		t.Fatalf("expected request id req-push-input-close, got %q", ack.GetRequestId())
	}
}

func TestProcessMistTrigger_AllDurableTypesGetAck(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	mistTriggerProcessor = &captureMistTriggerProcessor{}
	localClusterID = "cluster-local"

	durableTypes := []mist.TriggerType{
		mist.TriggerUserEnd,
		mist.TriggerStreamEnd,
		mist.TriggerPushEnd,
		mist.TriggerPushInputClose,
		mist.TriggerRecordingEnd,
		mist.TriggerRecordingSegment,
		mist.TriggerLivepeerSegmentComplete,
		mist.TriggerProcessAVSegmentComplete,
	}
	for _, triggerType := range durableTypes {
		triggerType := triggerType
		t.Run(string(triggerType), func(t *testing.T) {
			stream := &captureStream{}
			requestID := "req-" + string(triggerType)
			processMistTrigger(&ipcpb.MistTrigger{
				TriggerType: string(triggerType),
				Blocking:    false,
				RequestId:   requestID,
				TriggerPayload: &ipcpb.MistTrigger_RawMistWebhook{
					RawMistWebhook: &ipcpb.RawMistWebhookTrigger{PayloadRaw: []byte("raw")},
				},
			}, "node-1", stream, logging.Logger(logrus.New()))

			msg := stream.lastSent()
			if msg == nil {
				t.Fatal("expected durable ack")
			}
			ack := msg.GetMistTriggerAck()
			if ack == nil {
				t.Fatalf("expected MistTriggerAck, got %T", msg.GetPayload())
			}
			if !ack.GetSuccess() || ack.GetRetryable() || ack.GetErrorCode() != ipcpb.TriggerAckErrorCode_TRIGGER_ACK_ERROR_NONE {
				t.Fatalf("unexpected ack: success=%v retryable=%v code=%s message=%q", ack.GetSuccess(), ack.GetRetryable(), ack.GetErrorCode(), ack.GetErrorMessage())
			}
			if ack.GetRequestId() != requestID {
				t.Fatalf("expected request id %q, got %q", requestID, ack.GetRequestId())
			}
		})
	}
}

func TestProcessMistTrigger_NonDurableAsyncGetsNoAck(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	mistTriggerProcessor = &captureMistTriggerProcessor{}
	localClusterID = "cluster-local"
	stream := &captureStream{}

	processMistTrigger(&ipcpb.MistTrigger{
		TriggerType: string(mist.TriggerThumbnailUpdated),
		Blocking:    false,
		RequestId:   "req-thumbnail",
		TriggerPayload: &ipcpb.MistTrigger_RawMistWebhook{
			RawMistWebhook: &ipcpb.RawMistWebhookTrigger{PayloadRaw: []byte("raw")},
		},
	}, "node-1", stream, logging.Logger(logrus.New()))

	if msg := stream.lastSent(); msg != nil {
		t.Fatalf("non-durable async trigger should not receive a control-stream ack, got %T", msg.GetPayload())
	}
}

func TestProcessMistTrigger_DropsStaleControlStream(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	currentStream := &captureStream{}
	staleStream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", currentStream)
	t.Cleanup(cleanup)

	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	trigger := &ipcpb.MistTrigger{
		TriggerType: "USER_END",
		Blocking:    false,
		RequestId:   "req-stale",
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{
			ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{StreamName: "live+abc"},
		},
	}

	processMistTrigger(trigger, "node-1", staleStream, logging.Logger(logrus.New()))

	if capture.last != nil {
		t.Fatal("processor received stale trigger")
	}
	msg := staleStream.lastSent()
	if msg == nil {
		t.Fatal("expected durable ack for stale trigger")
	}
	ack := msg.GetMistTriggerAck()
	if ack == nil {
		t.Fatalf("expected MistTriggerAck, got %T", msg.GetPayload())
	}
	if ack.GetSuccess() {
		t.Fatal("expected stale trigger ack to fail")
	}
	if ack.GetRequestId() != "req-stale" {
		t.Fatalf("expected request id req-stale, got %q", ack.GetRequestId())
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

	trigger := &ipcpb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-3",
		TriggerPayload: &ipcpb.MistTrigger_PushEnd{
			PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+abc"},
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
	trigger := &ipcpb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-2",
		ClusterId:   &providedCluster,
		TriggerPayload: &ipcpb.MistTrigger_PushEnd{
			PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+abc"},
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
	trigger := &ipcpb.MistTrigger{
		TriggerType: "PUSH_END",
		Blocking:    false,
		RequestId:   "req-4",
		ClusterId:   &providedCluster,
		TriggerPayload: &ipcpb.MistTrigger_PushEnd{
			PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+abc"},
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
