package control

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"frameworks/api_balancing/internal/ingesterrors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type publisherAdmissionProcessor interface {
	ProcessPublisher(context.Context, *ipcpb.MistTrigger) (string, bool, error)
	PublisherAdmissionEnabled() bool
}

var publisherAdmissionSlots = make(chan struct{}, 64)
var publisherAdmissionWaiters = make(chan struct{}, 128)

func acquirePublisherAdmission(ctx context.Context) (func(), error) {
	started := time.Now()
	select {
	case publisherAdmissionWaiters <- struct{}{}:
	default:
		return nil, status.Error(codes.ResourceExhausted, "publisher admission queue full")
	}
	defer func() { <-publisherAdmissionWaiters }()
	select {
	case publisherAdmissionSlots <- struct{}{}:
		ObservePublisherAdmissionPhase("queue", started, nil)
		return func() { <-publisherAdmissionSlots }, nil
	case <-ctx.Done():
		err := status.FromContextError(ctx.Err()).Err()
		ObservePublisherAdmissionPhase("queue", started, err)
		return nil, err
	}
}

// AdmitPublisher requires a capability issued only after node-key registration,
// and fences the exact live control connection before and after admission.
func (s *Server) AdmitPublisher(ctx context.Context, req *ipcpb.AdmitPublisherRequest) (*ipcpb.MistTriggerResponse, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, status.Error(codes.InvalidArgument, "publisher admission requires a caller deadline")
	}
	if registry == nil || req.GetNodeId() == "" || len(req.GetSessionToken()) != 32 {
		return nil, status.Error(codes.Unauthenticated, "publisher node session required")
	}
	registry.mu.RLock()
	c := registry.conns[req.GetNodeId()]
	registry.mu.RUnlock()
	if c == nil || c.stream == nil || c.superseded.Load() || len(c.admissionToken) != 32 || subtle.ConstantTimeCompare(c.admissionToken, req.GetSessionToken()) != 1 || !publisherConnectionCurrent(req.GetNodeId(), c) {
		return nil, status.Error(codes.Unauthenticated, "publisher node session expired")
	}
	trigger := proto.CloneOf(req.GetTrigger())
	if trigger == nil || trigger.GetTriggerType() != "PUSH_REWRITE" || trigger.GetPushRewrite() == nil {
		return nil, status.Error(codes.InvalidArgument, "publisher admission requires PUSH_REWRITE")
	}
	processor, ok := mistTriggerProcessor.(publisherAdmissionProcessor)
	if !ok || !processor.PublisherAdmissionEnabled() {
		return nil, status.Error(codes.Unavailable, "publisher admission unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.stream.Context(), cancel)
	defer stop()
	release, err := acquirePublisherAdmission(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	trigger.NodeId = c.session().NodeID()
	trigger.ClusterId = stringPtrIfNotEmpty(c.clusterID)
	trigger.ControlCellId = stringPtrIfNotEmpty(localControlCellID)
	trigger.TenantId, trigger.UserId, trigger.StreamId, trigger.OriginClusterId = nil, nil, nil, nil
	text, abort, err := processor.ProcessPublisher(ctx, trigger)
	if !publisherConnectionCurrent(req.GetNodeId(), c) {
		return nil, status.Error(codes.Unavailable, "publisher connection replaced")
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	response := &ipcpb.MistTriggerResponse{RequestId: trigger.GetRequestId(), Response: text, Abort: abort}
	if err != nil {
		response.Response = ""
		response.Abort = true
		response.ErrorCode = ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL
		if ingestErr, ok := errors.AsType[*ingesterrors.IngestError](err); ok {
			response.ErrorCode = ingestErr.Code
		}
	}
	if response.Abort {
		response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_DENY
	} else {
		response.Action = ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE
		response.IngestGeneration = trigger.GetEventId()
		response.IngestConnectorPid = trigger.GetPushRewrite().GetPid()
	}
	return response, nil
}

func publisherConnectionCurrent(nodeID string, c *conn) bool {
	return c != nil && c.session().NodeID() == nodeID && c.clusterID != "" && c.fence > 0 && !c.superseded.Load() && connIsCurrentlyRegistered(nodeID, c)
}

type publisherSettlementCompatibilityKey struct{}

// PublisherSettlementRequiresCompatibility identifies an edge whose refusal
// handling predates durable settlement of post-lease domain denials.
func PublisherSettlementRequiresCompatibility(ctx context.Context) bool {
	compatible, ok := ctx.Value(publisherSettlementCompatibilityKey{}).(bool)
	return ok && compatible
}

// LegacyPublisherContext carries the legacy transport's caller deadline and
// settlement capability. Older edges omit the co-versioned deadline field and
// require synchronous known-denial cleanup before their response is returned.
func LegacyPublisherContext(parent context.Context, trigger *ipcpb.MistTrigger) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(mist.PublisherResponseTimeout)
	supplied := trigger.GetPublisherDeadline()
	capable := supplied != nil && supplied.IsValid()
	if capable && supplied.AsTime().Before(deadline) {
		deadline = supplied.AsTime()
	}
	parent = context.WithValue(parent, publisherSettlementCompatibilityKey{}, !capable)
	return context.WithDeadline(parent, deadline)
}
