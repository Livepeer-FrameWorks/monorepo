package control

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

type stopSessionsAuthorization struct {
	connection *streamConn
	nonce      string
	tenant     string
	names      []string
	accepted   bool
	permit     chan *ipcpb.StopSessionsRequest
}

var stopSessionsAuthorizations = struct {
	sync.Mutex
	byID map[string]*stopSessionsAuthorization
}{byID: make(map[string]*stopSessionsAuthorization)}

func acceptStopSessionsAuthorization(req *ipcpb.StopSessionsRequest, connection *streamConn) bool {
	stopSessionsAuthorizations.Lock()
	defer stopSessionsAuthorizations.Unlock()
	waiter := stopSessionsAuthorizations.byID[req.GetRequestId()]
	if waiter == nil || waiter.accepted || waiter.connection != connection || !stopSessionsNodeMatches(req, connection) ||
		waiter.nonce != req.GetAuthorizationNonce() || waiter.tenant != req.GetTenantId() || !slices.Equal(waiter.names, req.GetStreamNames()) {
		return false
	}
	waiter.accepted = true
	waiter.permit <- req
	return true
}

func authorizeStopSessions(ctx context.Context, req *ipcpb.StopSessionsRequest, connection *streamConn) (time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !stopSessionsNodeMatches(req, connection) {
		return 0, errors.New("session termination connection is no longer current")
	}
	waiter := &stopSessionsAuthorization{connection: connection, nonce: uuid.NewString(), tenant: req.GetTenantId(), names: slices.Clone(req.GetStreamNames()), permit: make(chan *ipcpb.StopSessionsRequest, 1)}
	stopSessionsAuthorizations.Lock()
	if stopSessionsAuthorizations.byID[req.GetRequestId()] != nil {
		stopSessionsAuthorizations.Unlock()
		return 0, errors.New("session termination authorization already pending")
	}
	stopSessionsAuthorizations.byID[req.GetRequestId()] = waiter
	stopSessionsAuthorizations.Unlock()
	defer func() {
		stopSessionsAuthorizations.Lock()
		if stopSessionsAuthorizations.byID[req.GetRequestId()] == waiter {
			delete(stopSessionsAuthorizations.byID, req.GetRequestId())
		}
		stopSessionsAuthorizations.Unlock()
	}()
	started := time.Now()
	challenge := &ipcpb.StopSessionsResult{RequestId: req.GetRequestId(), TenantId: req.GetTenantId(), StreamNames: slices.Clone(req.GetStreamNames()), AuthorizationNonce: waiter.nonce}
	if err := sendStopSessionsMessage(ctx, connection, &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_StopSessionsResult{StopSessionsResult: challenge}}); err != nil {
		return 0, err
	}
	select {
	case permit := <-waiter.permit:
		// Charging the entire round trip is conservative even when the owner's
		// reply waited in a send queue. No wall-clock offset can renew its budget.
		remaining := time.Duration(permit.GetRemainingTimeoutMs())*time.Millisecond - time.Since(started)
		if remaining <= 0 || ctx.Err() != nil || !stopSessionsNodeMatches(req, connection) {
			return 0, errors.New("session termination permit expired in transit")
		}
		return remaining, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-connection.ended:
		return 0, errors.New("session termination connection ended")
	}
}

func sendStopSessionsMessage(ctx context.Context, connection *streamConn, message *ipcpb.ControlMessage) error {
	if stream, ok := connection.stream.(interface {
		SendContext(context.Context, *ipcpb.ControlMessage) error
	}); ok {
		return stream.SendContext(ctx, message)
	}
	return connection.stream.Send(message)
}
