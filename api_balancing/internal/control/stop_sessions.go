package control

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const StopSessionsAckProtocolMin int32 = 9

type stopSessionsWaiter struct {
	connection *conn
	tenant     string
	names      []string
	result     chan error
}

var stopSessionsWaiters = struct {
	sync.Mutex
	byID map[string]stopSessionsWaiter
}{byID: make(map[string]stopSessionsWaiter)}

// A result can settle only its exact request on the still-current node connection.
func acceptStopSessionsResult(result *ipcpb.StopSessionsResult, session NodeSession) bool {
	stopSessionsWaiters.Lock()
	waiter, ok := stopSessionsWaiters.byID[result.GetRequestId()]
	stopSessionsWaiters.Unlock()
	if !ok || !connIsCurrentlyRegistered(session.NodeID(), waiter.connection) || waiter.connection.superseded.Load() ||
		waiter.connection.fence != session.Fence || waiter.connection.clusterID != session.ClusterID ||
		result.GetTenantId() != waiter.tenant || !slices.Equal(result.GetStreamNames(), waiter.names) {
		return false
	}
	var resultErr error
	if result.GetError() != "" {
		resultErr = fmt.Errorf("mist session stop was not confirmed: %s", result.GetError())
	}
	select {
	case waiter.result <- resultErr:
		return true
	default:
		return false
	}
}

// SendLocalStopSessionsContext waits for the edge's verified effect, rather than transport acceptance.
func SendLocalStopSessionsContext(ctx context.Context, nodeID string, req *ipcpb.StopSessionsRequest) error {
	if registry == nil {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	if req.GetTenantId() == "" || len(req.GetStreamNames()) == 0 {
		return errors.New("session termination requires tenant and stream scope")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if !c.features().ConfirmedSessionStop {
		// Old edges can still perform their best-effort stop during rollout,
		// but its transport acceptance cannot complete a durable revocation.
		legacy := &ipcpb.StopSessionsRequest{TenantId: req.TenantId, StreamNames: slices.Clone(req.StreamNames), Reason: req.Reason}
		if err := sendOnConnBounded(ctx, nodeID, c, &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_StopSessionsRequest{StopSessionsRequest: legacy}}, 3*time.Second); err != nil {
			return err
		}
		return errors.New("edge cannot acknowledge session termination; revocation remains pending until upgrade")
	}
	command := proto.CloneOf(req)
	command.RequestId = uuid.NewString()
	command.NodeId = nodeID
	deadline, _ := ctx.Deadline()
	command.ExpiresAt = timestamppb.New(deadline)
	command.Targets = nil
	for _, name := range command.StreamNames {
		if name == "" {
			return errors.New("empty stream in session termination scope")
		}
		generation := ""
		if db != nil {
			var err error
			generation, err = foghorndb.New(db).GetStopSessionsGeneration(ctx, foghorndb.GetStopSessionsGenerationParams{TenantID: command.TenantId, NodeID: nodeID, InternalName: name})
			if err != nil {
				return fmt.Errorf("capture session termination generation: %w", err)
			}
		}
		command.Targets = append(command.Targets, &ipcpb.StopSessionsTarget{InternalName: name, SourceGeneration: generation})
	}
	waiter := stopSessionsWaiter{connection: c, tenant: command.TenantId, names: slices.Clone(command.StreamNames), result: make(chan error, 1)}
	stopSessionsWaiters.Lock()
	stopSessionsWaiters.byID[command.RequestId] = waiter
	stopSessionsWaiters.Unlock()
	defer func() {
		stopSessionsWaiters.Lock()
		delete(stopSessionsWaiters.byID, command.RequestId)
		stopSessionsWaiters.Unlock()
	}()
	if err := sendOnConnBounded(ctx, nodeID, c, &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_StopSessionsRequest{StopSessionsRequest: command}}, 3*time.Second); err != nil {
		return err
	}
	select {
	case err := <-waiter.result:
		if !connIsCurrentlyRegistered(nodeID, c) || c.superseded.Load() {
			return ErrConnSuperseded
		}
		return err
	case <-ctx.Done():
		return fmt.Errorf("session termination acknowledgement: %w", ctx.Err())
	}
}
