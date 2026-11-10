package control

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

func TestStopSessionsAuthorizationRequiresLiveWaiterAndRemainingBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := &stopResultStream{captureStream: &captureStream{}}
		t.Cleanup(SetupTestRegistry("edge-stop", stream))
		previous := db
		db = nil
		t.Cleanup(func() { db = previous })
		nonce := uuid.NewString()
		permitSeen := false
		stream.afterSend = func(request *ipcpb.StopSessionsRequest) {
			if request.GetAuthorizationNonce() == "" {
				time.Sleep(2 * time.Second)
				session, _ := currentNodeSession("edge-stop")
				if acceptStopSessionsResult(&ipcpb.StopSessionsResult{RequestId: request.GetRequestId(), TenantId: request.GetTenantId(), StreamNames: request.GetStreamNames()}, session) {
					t.Error("effect result was accepted before current-owner authorization")
				}
				challenge := &ipcpb.StopSessionsResult{RequestId: request.GetRequestId(), TenantId: request.GetTenantId(), StreamNames: request.GetStreamNames(), AuthorizationNonce: nonce}
				if !acceptStopSessionsResult(challenge, session) || acceptStopSessionsResult(challenge, session) {
					t.Error("current challenge was refused or replayed")
					return
				}
			} else {
				permitSeen = true
				if request.GetAuthorizationNonce() != nonce || request.GetRemainingTimeoutMs() == 0 || request.GetRemainingTimeoutMs() > 8000 {
					t.Errorf("permit renewed its owner deadline: %v", request)
				}
				CompleteTestStopSessions("edge-stop", request, "")
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		err := SendStopSessionsContext(ctx, "edge-stop", &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}})
		synctest.Wait()
		if err != nil || !permitSeen {
			t.Fatalf("live stop did not authorize and verify: permit=%v err=%v", permitSeen, err)
		}
	})
}

func TestStopSessionsCancelledWaiterCannotAuthorizeDelayedCommand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stream := &stopResultStream{captureStream: &captureStream{}}
		t.Cleanup(SetupTestRegistry("edge-stop", stream))
		previous := db
		db = nil
		t.Cleanup(func() { db = previous })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var challenge *ipcpb.StopSessionsResult
		finished := make(chan struct{})
		stream.afterSend = func(request *ipcpb.StopSessionsRequest) {
			defer close(finished)
			cancel()
			session, _ := currentNodeSession("edge-stop")
			challenge = &ipcpb.StopSessionsResult{RequestId: request.GetRequestId(), TenantId: request.GetTenantId(), StreamNames: request.GetStreamNames(), AuthorizationNonce: uuid.NewString()}
			if acceptStopSessionsResult(challenge, session) {
				t.Error("cancelled owner issued a permit")
			}
		}
		if err := SendStopSessionsContext(ctx, "edge-stop", &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled stop result: %v", err)
		}
		<-finished
		synctest.Wait()
		session, _ := currentNodeSession("edge-stop")
		if challenge == nil || acceptStopSessionsResult(challenge, session) || len(stream.sent) != 1 {
			t.Fatal("removed owner waiter authorized delayed work")
		}
	})
}

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
		if req.GetAuthorizationNonce() == "" {
			CompleteTestStopSessions("edge-stop", req, "")
			return
		}
		calls++
		if req.GetRequestId() == "" || req.GetNodeId() != "edge-stop" || req.GetRemainingTimeoutMs() == 0 || len(req.GetTargets()) != 1 {
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
	synctest.Test(t, func(t *testing.T) {
		stream := &stopResultStream{captureStream: &captureStream{}}
		t.Cleanup(SetupTestRegistry("edge-stop", stream))
		previousDB := db
		db = nil
		t.Cleanup(func() { db = previousDB })
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		finished := make(chan struct{})
		stream.afterSend = func(req *ipcpb.StopSessionsRequest) {
			if req.GetAuthorizationNonce() == "" {
				CompleteTestStopSessions("edge-stop", req, "")
				return
			}
			defer close(finished)
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
		<-finished
		synctest.Wait()
	})
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
