package control

import (
	"context"
	"errors"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestStopSessionsTransportSendIsNotCompletion(t *testing.T) {
	stream := &stopResultStream{captureStream: &captureStream{}}
	t.Cleanup(SetupTestRegistry("edge-stop", stream))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream.afterSend = func(*ipcpb.StopSessionsRequest) { cancel() }
	if err := SendStopSessionsContext(ctx, "edge-stop", &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}); err == nil {
		t.Fatal("successful control-stream send without a Mist acknowledgment completed revocation")
	}
	if len(stream.sent) != 1 {
		t.Fatal("test did not exercise a successful stream send")
	}
}

type stopResultStream struct {
	*captureStream
	afterSend func(*ipcpb.StopSessionsRequest)
}

func (s *stopResultStream) Send(msg *ipcpb.ControlMessage) error {
	if err := s.captureStream.Send(msg); err != nil {
		return err
	}
	if req := msg.GetStopSessionsRequest(); req != nil && s.afterSend != nil {
		s.afterSend(req)
	}
	return nil
}

func TestStopSessionsFailureRemainsRetryableAndSuccessIsCorrelated(t *testing.T) {
	stream := &stopResultStream{captureStream: &captureStream{}}
	t.Cleanup(SetupTestRegistry("edge-stop", stream))
	previousDB := db
	db = nil
	t.Cleanup(func() { db = previousDB })
	calls := 0
	stream.afterSend = func(req *ipcpb.StopSessionsRequest) {
		calls++
		if req.GetRequestId() == "" || req.GetNodeId() != "edge-stop" || req.GetExpiresAt() == nil || len(req.GetTargets()) != 1 {
			t.Error("stop request omitted its exact execution scope")
		}
		errorText := "temporary Mist API failure"
		if calls > 1 {
			errorText = ""
		}
		if !CompleteTestStopSessions("edge-stop", req, errorText) {
			t.Error("current edge result was rejected")
		}
	}
	req := &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}
	if err := SendStopSessionsContext(t.Context(), "edge-stop", req); err == nil {
		t.Fatal("temporary Mist failure completed revocation")
	}
	if err := SendStopSessionsContext(t.Context(), "edge-stop", req); err != nil {
		t.Fatalf("fresh retry after verified stop failed: %v", err)
	}
	if req.GetRequestId() != "" || len(req.GetTargets()) != 0 {
		t.Fatal("dispatch mutated caller-owned request")
	}
}

func TestStopSessionsRejectsWrongScopeAndRetiredConnection(t *testing.T) {
	stream := &stopResultStream{captureStream: &captureStream{}}
	t.Cleanup(SetupTestRegistry("edge-stop", stream))
	previousDB := db
	db = nil
	t.Cleanup(func() { db = previousDB })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream.afterSend = func(req *ipcpb.StopSessionsRequest) {
		session, _ := currentNodeSession("edge-stop")
		result := &ipcpb.StopSessionsResult{RequestId: req.RequestId, TenantId: "other-tenant", StreamNames: req.StreamNames}
		if acceptStopSessionsResult(result, session) {
			t.Error("foreign tenant completed revocation")
		}
		result.TenantId, result.StreamNames = req.TenantId, []string{"other-stream"}
		if acceptStopSessionsResult(result, session) {
			t.Error("foreign stream completed revocation")
		}
		result.StreamNames = req.StreamNames
		registry.mu.Lock()
		registry.conns["edge-stop"] = &conn{stream: &captureStream{}, rawNodeID: "edge-stop", fence: session.Fence + 1}
		registry.mu.Unlock()
		if acceptStopSessionsResult(result, session) {
			t.Error("retired control connection completed revocation")
		}
		cancel()
	}
	if err := SendStopSessionsContext(ctx, "edge-stop", &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("unacknowledged stale result did not remain pending: %v", err)
	}
}

func TestStopSessionsOlderEdgeBestEffortRemainsPending(t *testing.T) {
	stream := &captureStream{}
	t.Cleanup(SetupTestRegistry("legacy-edge", stream))
	registry.conns["legacy-edge"].protocolVersion = StopSessionsAckProtocolMin - 1
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := SendStopSessionsContext(ctx, "legacy-edge", &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}); err == nil {
		t.Fatal("legacy transport-only edge completed revocation")
	}
	if len(stream.sent) != 1 || stream.sent[0].GetStopSessionsRequest().GetRequestId() != "" {
		t.Fatal("legacy edge did not receive its best-effort compatibility command")
	}
}
