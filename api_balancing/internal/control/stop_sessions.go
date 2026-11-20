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

const StopSessionsAckProtocolMin int32 = 10
const ClaimedPublisherStopProtocolMin int32 = 12

type stopSessionsWaiter struct {
	connection *conn
	tenant     string
	names      []string
	result     chan error
	ctx        context.Context
	command    *ipcpb.StopSessionsRequest
	authorized string
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
	if waiter.ctx.Err() != nil {
		return false
	}
	if nonce := result.GetAuthorizationNonce(); nonce != "" {
		if _, err := uuid.Parse(nonce); err != nil || result.GetError() != "" {
			return false
		}
		stopSessionsWaiters.Lock()
		current, exists := stopSessionsWaiters.byID[result.GetRequestId()]
		if !exists || current.result != waiter.result || current.authorized != "" || current.ctx.Err() != nil {
			stopSessionsWaiters.Unlock()
			return false
		}
		current.authorized = nonce
		stopSessionsWaiters.byID[result.GetRequestId()] = current
		stopSessionsWaiters.Unlock()
		go func() {
			permit := proto.CloneOf(waiter.command)
			deadline, _ := waiter.ctx.Deadline()
			remaining := time.Until(deadline).Milliseconds()
			if remaining <= 0 {
				return
			}
			permit.AuthorizationNonce = nonce
			permit.RemainingTimeoutMs = uint32(remaining)
			if err := sendOnConnBounded(waiter.ctx, session.NodeID(), waiter.connection, &ipcpb.ControlMessage{SentAt: timestamppb.Now(), Payload: &ipcpb.ControlMessage_StopSessionsRequest{StopSessionsRequest: permit}}, 3*time.Second); err != nil {
				select {
				case waiter.result <- err:
				default:
				}
			}
		}()
		return true
	}
	if waiter.authorized == "" {
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
	return sendLocalStopSessionsContext(ctx, nodeID, req, false)
}

func SendLocalClaimedStopSessionsContext(ctx context.Context, nodeID string, req *ipcpb.StopSessionsRequest) error {
	if len(req.GetTargets()) != len(req.GetStreamNames()) || len(req.GetTargets()) == 0 {
		return errors.New("claim-fenced stop requires exact generation targets")
	}
	for index, target := range req.GetTargets() {
		if target.GetInternalName() != req.GetStreamNames()[index] {
			return errors.New("claim-fenced stop target differs from stream scope")
		}
		if _, err := uuid.Parse(target.GetSourceGeneration()); err != nil {
			return errors.New("claim-fenced stop requires a session generation")
		}
		if !target.GetFenceAdmission() || target.GetConnectorPid() <= 0 {
			return errors.New("claim-fenced stop requires durable admission rejection and connector identity")
		}
	}
	return sendLocalStopSessionsContext(ctx, nodeID, req, true)
}

func sendLocalStopSessionsContext(ctx context.Context, nodeID string, req *ipcpb.StopSessionsRequest, claimed bool) error {
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
	if claimed && !c.features().ClaimedPublisherStop {
		return errors.New("claimed publisher stop requires an edge with durable admission rejection")
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if !c.features().ConfirmedSessionStop {
		if claimed {
			return errors.New("claim-fenced stop requires a generation-aware edge")
		}
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
	remaining := time.Until(deadline).Milliseconds()
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	command.RemainingTimeoutMs = uint32(remaining)
	command.AuthorizationNonce = ""
	if !claimed {
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
	}

	waiter := stopSessionsWaiter{connection: c, tenant: command.TenantId, names: slices.Clone(command.StreamNames), result: make(chan error, 1), ctx: ctx, command: command}
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
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !connIsCurrentlyRegistered(nodeID, c) || c.superseded.Load() {
			return ErrConnSuperseded
		}
		return err
	case <-ctx.Done():
		return fmt.Errorf("session termination acknowledgement: %w", ctx.Err())
	}
}
