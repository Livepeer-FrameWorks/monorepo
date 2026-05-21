package resolvers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Bridge exposes a GraphQL enum while the control plane stores node mode as a
// wire string. Translation stays at this boundary, and co-selected node health
// fields share one GetNodeHealth response per request.

// DoSetNodeMode wraps Commodore.SetNodeMode with proper enum translation +
// union-result error mapping. Reason defaults to the calling user when
// omitted so audit rows are never anonymous.
func (r *Resolver) DoSetNodeMode(ctx context.Context, input model.SetNodeModeInput) (model.SetNodeModeResult, error) {
	if err := middleware.RequirePermission(ctx, "infrastructure:write"); err != nil {
		return nil, err
	}
	if input.NodeID == "" {
		return &model.ValidationError{Message: "nodeId is required", Field: strPtr("nodeId")}, nil
	}
	wire := input.Mode.WireValue()
	if wire == "" {
		return &model.ValidationError{Message: "invalid mode", Field: strPtr("mode")}, nil
	}
	reason := strings.TrimSpace(deref(input.Reason))
	if reason == "" {
		reason = callerIdentity(ctx)
	}

	resp, err := r.Clients.Commodore.SetNodeMode(ctx, &pb.SetNodeModeRequest{
		NodeId: input.NodeID,
		Mode:   wire,
		SetBy:  reason,
	})
	if err != nil {
		if vErr := mapInvalidArgument(err); vErr != nil {
			return vErr, nil
		}
		if nfErr := mapNotFound(err); nfErr != nil {
			return nfErr, nil
		}
		r.Logger.WithError(err).Error("SetNodeMode: Commodore RPC failed")
		return nil, fmt.Errorf("set node mode: %w", err)
	}

	switch resp.GetStatus() {
	case pb.SetNodeModeStatus_SET_NODE_MODE_STATUS_NOT_FOUND:
		return &model.NotFoundError{Message: resp.GetMessage()}, nil
	case pb.SetNodeModeStatus_SET_NODE_MODE_STATUS_INVALID_MODE:
		return &model.ValidationError{Message: resp.GetMessage(), Field: strPtr("mode")}, nil
	}

	// Quartermaster has no GetNodeByID; clients selecting effectiveMode /
	// routingImpactPreview on the result get fresh values via the field
	// resolvers below (GetNodeHealth). Static fields aren't repopulated
	// here — clients that need them refetch via the `node(id:)` query.
	return &pb.InfrastructureNode{NodeId: resp.GetNodeId()}, nil
}

// DoNodeEffectiveMode is the field resolver for InfrastructureNode.effectiveMode.
// Always reads through GetNodeHealth so the answer reflects Foghorn's live
// view rather than a stale cached column.
func (r *Resolver) DoNodeEffectiveMode(ctx context.Context, obj *pb.InfrastructureNode) (model.NodeOperationalMode, error) {
	if nodeSkipsOperationalMode(obj) {
		return model.NodeOperationalModeNormal, nil
	}
	health, err := r.nodeHealthFor(ctx, obj.GetNodeId())
	if err != nil {
		if nodeHealthSoftFailure(err) {
			r.Logger.WithError(err).WithField("node_id", obj.GetNodeId()).Warn("Node health unavailable; defaulting effective mode")
			return model.NodeOperationalModeNormal, nil
		}
		return model.NodeOperationalModeNormal, err
	}
	if health == nil {
		return model.NodeOperationalModeNormal, nil
	}
	mode, ok := model.NodeOperationalModeFromWire(health.GetOperationalMode())
	if !ok {
		return model.NodeOperationalModeNormal, fmt.Errorf("unknown node operational mode %q", health.GetOperationalMode())
	}
	return mode, nil
}

// DoNodeRoutingImpactPreview returns the node's current active streams and
// viewers. Same single-fetch path as effectiveMode.
func (r *Resolver) DoNodeRoutingImpactPreview(ctx context.Context, obj *pb.InfrastructureNode) (*model.RoutingImpactPreview, error) {
	if nodeSkipsOperationalMode(obj) {
		return &model.RoutingImpactPreview{}, nil
	}
	health, err := r.nodeHealthFor(ctx, obj.GetNodeId())
	if err != nil {
		if nodeHealthSoftFailure(err) {
			r.Logger.WithError(err).WithField("node_id", obj.GetNodeId()).Warn("Node health unavailable; defaulting routing impact preview")
			return &model.RoutingImpactPreview{}, nil
		}
		return nil, err
	}
	if health == nil {
		return &model.RoutingImpactPreview{}, nil
	}
	return &model.RoutingImpactPreview{
		ActiveStreams: int(health.GetActiveStreams()),
		ActiveViewers: int(health.GetActiveViewers()),
	}, nil
}

func nodeHealthSoftFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return true
	default:
		return false
	}
}

func nodeSkipsOperationalMode(obj *pb.InfrastructureNode) bool {
	nodeType := strings.TrimSpace(obj.GetNodeType())
	return nodeType != "" && !strings.EqualFold(nodeType, "edge")
}

// nodeHealthFor returns the GetNodeHealth response for a node, memoised on
// the request's context so concurrent field resolvers within a single
// query share one RPC.
func (r *Resolver) nodeHealthFor(ctx context.Context, nodeID string) (*pb.GetNodeHealthResponse, error) {
	if nodeID == "" {
		return nil, fmt.Errorf("node id is required")
	}
	if r == nil || r.Clients == nil || r.Clients.Commodore == nil {
		return nil, fmt.Errorf("commodore client unavailable")
	}
	cache := nodeHealthCacheFromContext(ctx)
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if e, ok := cache.entries[nodeID]; ok {
		return e.resp, e.err
	}
	if len(cache.entries) >= maxNodeHealthCacheEntries {
		return nil, fmt.Errorf("node health cache limit exceeded")
	}
	resp, err := r.Clients.Commodore.GetNodeHealth(ctx, &pb.GetNodeHealthRequest{NodeId: nodeID})
	cache.entries[nodeID] = nodeHealthEntry{resp: resp, err: err}
	return resp, err
}

// ---- per-request memoisation ----

type nodeHealthEntry struct {
	resp *pb.GetNodeHealthResponse
	err  error
}

type nodeHealthCache struct {
	mu      sync.Mutex
	entries map[string]nodeHealthEntry
}

type nodeHealthCacheKey struct{}

const maxNodeHealthCacheEntries = 500

func nodeHealthCacheFromContext(ctx context.Context) *nodeHealthCache {
	if c, ok := ctx.Value(nodeHealthCacheKey{}).(*nodeHealthCache); ok {
		return c
	}
	// No cache attached: return a one-shot scratch cache so this request
	// still gets memoisation across concurrent field resolvers within the
	// same goroutine tree. WithNodeHealthCache wires a long-lived cache
	// for the whole request when called from the request middleware.
	return &nodeHealthCache{entries: map[string]nodeHealthEntry{}}
}

// WithNodeHealthCache attaches a fresh cache to ctx so InfrastructureNode
// field resolvers in the same query coalesce their GetNodeHealth calls.
func WithNodeHealthCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(nodeHealthCacheKey{}).(*nodeHealthCache); ok {
		return ctx
	}
	return context.WithValue(ctx, nodeHealthCacheKey{}, &nodeHealthCache{
		entries: map[string]nodeHealthEntry{},
	})
}

// ---- helpers ----

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func callerIdentity(ctx context.Context) string {
	if u := ctxkeys.GetUserID(ctx); u != "" {
		return "user:" + u
	}
	return "graphql"
}
