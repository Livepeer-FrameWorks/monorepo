package control

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

type stopAuthorizationStream struct {
	ipcpb.HelmsmanControl_ConnectClient
	send func(*ipcpb.ControlMessage) error
}

func (s *stopAuthorizationStream) Send(message *ipcpb.ControlMessage) error {
	return s.send(message)
}

func authorizationTestConnection(t *testing.T, stream *stopAuthorizationStream) *streamConn {
	t.Helper()
	connection := &streamConn{stream: stream, nodeID: "edge", epoch: "stop-test", ended: make(chan struct{})}
	previous := activeConn.Swap(connection)
	t.Cleanup(func() { activeConn.Store(previous) })
	return connection
}

func TestStopAuthorizationConsumesRoundTripAndRejectsReplayScopeAndReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := relativeStopFixtureRequest(t)
		stream := &stopAuthorizationStream{}
		connection := authorizationTestConnection(t, stream)
		stream.send = func(message *ipcpb.ControlMessage) error {
			challenge := message.GetStopSessionsResult()
			permit := proto.CloneOf(request)
			permit.AuthorizationNonce, permit.RemainingTimeoutMs = challenge.GetAuthorizationNonce(), 4000
			for _, mutate := range []func(*ipcpb.StopSessionsRequest){
				func(p *ipcpb.StopSessionsRequest) { p.AuthorizationNonce = "wrong-nonce" },
				func(p *ipcpb.StopSessionsRequest) { p.TenantId = "other-tenant" },
				func(p *ipcpb.StopSessionsRequest) { p.StreamNames = []string{"other-stream"} },
			} {
				wrong := proto.CloneOf(permit)
				mutate(wrong)
				if acceptStopSessionsAuthorization(wrong, connection) {
					t.Fatal("authorization accepted foreign scope")
				}
			}
			activeConn.Store(&streamConn{nodeID: "edge", epoch: "replacement"})
			if acceptStopSessionsAuthorization(permit, connection) {
				t.Fatal("retired connection accepted a permit")
			}
			activeConn.Store(connection)
			time.Sleep(time.Second)
			if !acceptStopSessionsAuthorization(permit, connection) || acceptStopSessionsAuthorization(permit, connection) {
				t.Fatal("valid permit was rejected or replayed")
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		remaining, err := authorizeStopSessions(ctx, request, connection)
		if err != nil || remaining != 3*time.Second {
			t.Fatalf("permit budget did not subtract the entire local round trip: remaining=%s err=%v", remaining, err)
		}
	})
}

func TestStopAuthorizationDelayedPermitCannotRenewOwnerBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := relativeStopFixtureRequest(t)
		stream := &stopAuthorizationStream{}
		connection := authorizationTestConnection(t, stream)
		stream.send = func(message *ipcpb.ControlMessage) error {
			permit := proto.CloneOf(request)
			permit.AuthorizationNonce = message.GetStopSessionsResult().GetAuthorizationNonce()
			permit.RemainingTimeoutMs = 2000
			time.Sleep(3 * time.Second)
			if !acceptStopSessionsAuthorization(permit, connection) {
				t.Fatal("test permit did not reach the pending request")
			}
			return nil
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if remaining, err := authorizeStopSessions(ctx, request, connection); err == nil || remaining != 0 {
			t.Fatalf("delayed permit extended an expired owner budget: remaining=%s err=%v", remaining, err)
		}
	})
}

func TestStopAuthorizationCancellationRemovesPendingNonce(t *testing.T) {
	request := relativeStopFixtureRequest(t)
	stream := &stopAuthorizationStream{}
	connection := authorizationTestConnection(t, stream)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var permit *ipcpb.StopSessionsRequest
	stream.send = func(message *ipcpb.ControlMessage) error {
		permit = proto.CloneOf(request)
		permit.AuthorizationNonce = message.GetStopSessionsResult().GetAuthorizationNonce()
		permit.RemainingTimeoutMs = 1000
		cancel()
		return nil
	}
	if _, err := authorizeStopSessions(ctx, request, connection); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authorization waited or succeeded: %v", err)
	}
	if acceptStopSessionsAuthorization(permit, connection) {
		t.Fatal("late permit revived a cancelled request")
	}
}
