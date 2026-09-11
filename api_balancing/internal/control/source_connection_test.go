package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestSourceConnectionRetryRechecksAdmission(t *testing.T) {
	previous := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = previous })
	resetBlockingTriggerReplayForTest(t)
	capture := &captureMistTriggerProcessor{response: "true"}
	mistTriggerProcessor = capture
	session := NodeSession{CanonicalNodeID: "real-source", ClusterID: "real-cluster"}
	trigger := &ipcpb.MistTrigger{TriggerType: "CONN_PLAY", Blocking: true, TriggerUuid: "same-connection", RequestId: "first",
		NodeId: "forged-source", ClusterId: proto.String("forged-cluster"),
		TriggerPayload: &ipcpb.MistTrigger_ConnectionPlay{ConnectionPlay: &ipcpb.ConnectionPlayTrigger{StreamName: "live+stream", Host: "192.0.2.1", Connector: "DTSC", RequestUrl: "dtsc://source/live+stream"}}}
	first := &captureStream{}
	t.Cleanup(SetupTestRegistry("real-source", first))
	registry.conns["real-source"].canonicalID = "real-source"
	registry.conns["real-source"].clusterID = "real-cluster"
	processMistTrigger(trigger, session, first, logging.NewLogger())
	if first.lastSent().GetMistTriggerResponse().GetResponse() != "true" || capture.last.GetNodeId() != "real-source" || capture.last.GetClusterId() != "real-cluster" {
		t.Fatal("source admission did not use authenticated connection identity")
	}
	capture.response = ""
	trigger.RequestId = "retry"
	second := &captureStream{}
	registry.conns["real-source"].stream = second
	processMistTrigger(trigger, session, second, logging.NewLogger())
	if capture.calls != 2 || second.lastSent().GetMistTriggerResponse().GetAction() != ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY {
		t.Fatal("source admission retry reused a prior allow or allowed an empty result")
	}
}

type sourceAdmissionProcessorFunc func(*ipcpb.MistTrigger) (string, bool, error)

func (f sourceAdmissionProcessorFunc) ProcessTypedTrigger(trigger *ipcpb.MistTrigger) (string, bool, error) {
	return f(trigger)
}
func (f sourceAdmissionProcessorFunc) ProcessTrigger(string, []byte, string) (string, bool, error) {
	panic("typed source hook required")
}

func TestSourceConnectionRejectsRetiredRegistration(t *testing.T) {
	previous := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = previous })
	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("raw-source", stream))
	registration := registry.conns["raw-source"]
	registration.canonicalID, registration.clusterID, registration.fence = "canonical-source", "cluster", 7
	session := NodeSession{RawNodeID: "raw-source", CanonicalNodeID: "canonical-source", ClusterID: "cluster", Fence: 7}
	if !sourceConnectionSessionCurrent(session, stream) {
		t.Fatal("canonical identity did not resolve through the raw registration key")
	}
	mistTriggerProcessor = sourceAdmissionProcessorFunc(func(*ipcpb.MistTrigger) (string, bool, error) {
		registry.mu.Lock()
		delete(registry.conns, "raw-source")
		registry.mu.Unlock()
		return "true", false, nil
	})
	processMistTrigger(&ipcpb.MistTrigger{TriggerType: "CONN_PLAY", Blocking: true, RequestId: "retired"}, session, stream, logging.NewLogger())
	if !stream.lastSent().GetMistTriggerResponse().GetAbort() {
		t.Fatal("registration disappeared during authorization but admission succeeded")
	}
}

func TestStreamSourceRetryReadsCurrentResolution(t *testing.T) {
	previous := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = previous })
	resetBlockingTriggerReplayForTest(t)
	capture := &captureMistTriggerProcessor{response: "dtsc://source/live+stream"}
	mistTriggerProcessor = capture
	session := NodeSession{CanonicalNodeID: "edge", ClusterID: "cluster"}
	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("edge", stream))
	registry.conns["edge"].canonicalID, registry.conns["edge"].clusterID = "edge", "cluster"
	trigger := &ipcpb.MistTrigger{TriggerType: "STREAM_SOURCE", Blocking: true, TriggerUuid: "same-source-open", RequestId: "first",
		NodeId: "forged-edge", ClusterId: proto.String("forged-cluster"),
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "live+stream"}}}
	processMistTrigger(trigger, session, stream, logging.NewLogger())
	if stream.lastSent().GetMistTriggerResponse().GetResponse() != capture.response || capture.last.GetNodeId() != "edge" || capture.last.GetClusterId() != "cluster" {
		t.Fatal("source resolution lost authenticated identity")
	}
	capture.response = OfflineNotPlaced
	trigger.RequestId = "retry"
	processMistTrigger(trigger, session, stream, logging.NewLogger())
	response := stream.lastSent().GetMistTriggerResponse()
	if capture.calls != 2 || response.GetResponse() != OfflineNotPlaced || response.GetAction() != ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE {
		t.Fatal("source retry replayed a cached URL or enabled configured-source fallback")
	}
	blockingTriggerReplay.Lock()
	entries := len(blockingTriggerReplay.entries)
	blockingTriggerReplay.Unlock()
	if entries != 0 {
		t.Fatal("source resolution was retained in the mutation replay cache")
	}
}

func TestStreamSourceRejectsRetirementDuringResolution(t *testing.T) {
	previous := mistTriggerProcessor
	t.Cleanup(func() { mistTriggerProcessor = previous })
	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("edge", stream))
	registry.conns["edge"].canonicalID, registry.conns["edge"].clusterID = "edge", "cluster"
	session := NodeSession{CanonicalNodeID: "edge", ClusterID: "cluster"}
	mistTriggerProcessor = sourceAdmissionProcessorFunc(func(*ipcpb.MistTrigger) (string, bool, error) {
		registry.mu.Lock()
		delete(registry.conns, "edge")
		registry.mu.Unlock()
		return "dtsc://source/live+stream", false, nil
	})
	processMistTrigger(&ipcpb.MistTrigger{TriggerType: "STREAM_SOURCE", Blocking: true, RequestId: "retired"}, session, stream, logging.NewLogger())
	response := stream.lastSent().GetMistTriggerResponse()
	if !response.GetAbort() || response.GetResponse() != "" {
		t.Fatal("retired registration received a usable source")
	}
}
