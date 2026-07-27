package control

import (
	"errors"
	"testing"

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

// There is NO local-cluster fallback: the cluster is whatever the authenticated session carries. A session
// with an empty cluster yields an empty cluster_id — the server never substitutes its own local cluster,
// which would misattribute a remote node's work to this Foghorn.
func TestProcessMistTrigger_NoLocalClusterFallback(t *testing.T) {
	prevProcessor := mistTriggerProcessor
	prevLocalClusterID := localClusterID
	t.Cleanup(func() {
		mistTriggerProcessor = prevProcessor
		localClusterID = prevLocalClusterID
	})

	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	trigger := pushEndTrigger("req-1", "")
	processMistTrigger(trigger, NodeSession{CanonicalNodeID: "node-1"}, nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != "" {
		t.Fatalf("empty session cluster must not fall back to local cluster, got %q", capture.last.GetClusterId())
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

	processMistTrigger(trigger, NodeSession{CanonicalNodeID: "node-1"}, stream, logging.Logger(logrus.New()))

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

	processMistTrigger(trigger, NodeSession{CanonicalNodeID: "node-1"}, stream, logging.Logger(logrus.New()))

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
			}, NodeSession{CanonicalNodeID: "node-1"}, stream, logging.Logger(logrus.New()))

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
	}, NodeSession{CanonicalNodeID: "node-1"}, stream, logging.Logger(logrus.New()))

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

	processMistTrigger(trigger, NodeSession{CanonicalNodeID: "node-1"}, staleStream, logging.Logger(logrus.New()))

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

// registerTestConn installs an authenticated control connection (the source processMistTrigger now derives
// The server-owned NodeSession reflects the authenticated connection's resolved identity, prefers the canonical
// node id, and is absent for an unregistered node.
func TestNodeSession_FromRegisteredConn(t *testing.T) {
	prev := registry
	t.Cleanup(func() { registry = prev })
	registry = &Registry{conns: map[string]*conn{}, log: logging.Logger(logrus.New())}
	registry.conns["raw-1"] = &conn{rawNodeID: "raw-1", canonicalID: "canon-1", clusterID: "cluster-x", fence: 7, protocolVersion: 3}

	sess, ok := currentNodeSession("raw-1")
	if !ok {
		t.Fatal("expected a session for a registered node")
	}
	if sess.RawNodeID != "raw-1" || sess.CanonicalNodeID != "canon-1" || sess.ClusterID != "cluster-x" || sess.Fence != 7 || sess.ProtocolVersion != 3 {
		t.Fatalf("session mismatch: %+v", sess)
	}
	if sess.NodeID() != "canon-1" {
		t.Fatalf("NodeID() must prefer the canonical id, got %q", sess.NodeID())
	}

	// No canonical id → NodeID falls back to the raw registry key.
	registry.conns["raw-2"] = &conn{rawNodeID: "raw-2", clusterID: "c"}
	if s2, _ := currentNodeSession("raw-2"); s2.NodeID() != "raw-2" {
		t.Fatalf("NodeID() must fall back to the raw id, got %q", s2.NodeID())
	}

	// Unregistered node → no session.
	if _, ok := currentNodeSession("ghost"); ok {
		t.Fatal("an unregistered node must have no session")
	}

	// Pre-resolution non-dispatchability: the zero session has an empty NodeID(). The Connect receive loop
	// drops any trigger whose captured session reports an empty NodeID() (see the guard before dispatch), so
	// a connection that has not resolved+published its identity can never attribute work to any node.
	if (NodeSession{}).NodeID() != "" {
		t.Fatal("an unresolved (zero) session must report an empty NodeID() so the dispatch guard drops it")
	}
}

// pushEndTrigger builds a PUSH_END trigger optionally carrying a self-asserted payload cluster_id.
func pushEndTrigger(reqID, payloadCluster string) *ipcpb.MistTrigger {
	tr := &ipcpb.MistTrigger{
		TriggerType:    "PUSH_END",
		RequestId:      reqID,
		TriggerPayload: &ipcpb.MistTrigger_PushEnd{PushEnd: &ipcpb.PushEndTrigger{StreamName: "live+abc"}},
	}
	if payloadCluster != "" {
		tr.ClusterId = &payloadCluster
	}
	return tr
}

// The cluster stamped on a trigger comes from the AUTHENTICATED session passed by value, overriding any
// self-asserted payload cluster_id. There is no local-cluster fallback.
func TestProcessMistTrigger_UsesSessionClusterAuthoritatively(t *testing.T) {
	prev := mistTriggerProcessor
	prevLocal := localClusterID
	t.Cleanup(func() { mistTriggerProcessor = prev; localClusterID = prevLocal })
	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture
	localClusterID = "cluster-local"

	trigger := pushEndTrigger("req-1", "cluster-from-helmsman")
	processMistTrigger(trigger, NodeSession{CanonicalNodeID: "node-remote", ClusterID: "cluster-authenticated"}, nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != "cluster-authenticated" {
		t.Fatalf("cluster must come from the session, not the payload or local cluster; got %q", capture.last.GetClusterId())
	}
}

// The trigger is attributed to the session's CANONICAL node id, not the raw registry key and not a payload id.
func TestProcessMistTrigger_AttributesToCanonicalNodeID(t *testing.T) {
	prev := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = prev })
	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture

	trigger := pushEndTrigger("req-2", "")
	trigger.NodeId = "some-other-node"
	processMistTrigger(trigger, NodeSession{RawNodeID: "raw-x", CanonicalNodeID: "canon-x", ClusterID: "c"}, nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetNodeId() != "canon-x" {
		t.Fatalf("node_id must be the session canonical id, got %q", capture.last.GetNodeId())
	}
}

// Reconnect safety: the handler uses the session PASSED BY VALUE and never re-reads the registry, so a
// reconnect that has already replaced the registry entry (with a different cluster) cannot substitute the
// newer session for work received on the older connection.
func TestProcessMistTrigger_UsesPassedSessionNotRegistry(t *testing.T) {
	prev := mistTriggerProcessor
	prevReg := registry
	t.Cleanup(func() { mistTriggerProcessor = prev; registry = prevReg })
	capture := &captureMistTriggerProcessor{}
	mistTriggerProcessor = capture

	// A reconnect has already published a NEWER connection for node-x with a different cluster.
	registry = &Registry{conns: map[string]*conn{}, log: logging.Logger(logrus.New())}
	registry.conns["node-x"] = &conn{rawNodeID: "node-x", canonicalID: "node-x", clusterID: "cluster-NEW"}

	// Work received on the OLDER connection carries the older session by value.
	oldSession := NodeSession{CanonicalNodeID: "node-x", ClusterID: "cluster-OLD"}
	processMistTrigger(pushEndTrigger("req-3", ""), oldSession, nil, logging.Logger(logrus.New()))

	if capture.last == nil {
		t.Fatal("processor did not receive trigger")
	}
	if capture.last.GetClusterId() != "cluster-OLD" {
		t.Fatalf("handler must use the passed session (cluster-OLD), not the reconnected registry entry; got %q", capture.last.GetClusterId())
	}
}
