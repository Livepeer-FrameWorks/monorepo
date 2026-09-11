package federation

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MediaServePreparationRuntime dispatches serving preparation by signed object
// kind. A push stream keeps its arranged origin pull, because only placement can
// bind that cross-cell attempt. A configured input or a stored artifact is
// materialized by the destination's own source resolution, so preparation proves
// the destination may serve this exact generation and binds no pull; inventing a
// physical binding for those kinds would claim ownership no publisher holds.
type MediaServePreparationRuntime struct {
	CellID    string
	Authority PlacementAuthorityReader
	Paths     *MediaPlacementPaths
	Push      *LivePushPreparationRuntime
	Snapshot  func() *state.BalancerSnapshot
	Now       func() time.Time
}

var _ PlacementPreparationRuntime = (*MediaServePreparationRuntime)(nil)

type mediaPreparationState struct {
	node      state.EnhancedBalancerNodeSnapshot
	endpoint  string
	expiresAt time.Time
}

// PushPreparation returns the push half of a configured destination's serving
// runtime. Source admission binds to it because only a push stream has an
// arranged cross-cell pull to authorize; a destination that startup has not
// configured, or one whose serving runtime is not this dispatcher, returns nil so
// the caller refuses instead of guessing.
func (destination *PlacementDestination) PushPreparation() *LivePushPreparationRuntime {
	if destination == nil {
		return nil
	}
	policy, ok := destination.Runtime.(*PolicyBoundPlacementRuntime)
	if !ok || policy == nil {
		return nil
	}
	media, ok := policy.Media.(*PlacementMediaRuntime)
	if !ok || media == nil {
		return nil
	}
	serve, ok := media.Serve.(*MediaServePreparationRuntime)
	if !ok || serve == nil || serve.Paths == nil || serve.Push == nil || serve.Push.Paths != serve.Paths.Push {
		return nil
	}
	if destination.Discovery == nil || PlacementPathReader(serve.Paths) != destination.Discovery.Paths {
		return nil
	}
	return serve.Push
}

func (runtime *MediaServePreparationRuntime) now() time.Time {
	if runtime != nil && runtime.Now != nil {
		return runtime.Now().UTC()
	}
	return time.Now().UTC()
}

func (runtime *MediaServePreparationRuntime) Revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	authority, pair, push, err := runtime.classify(ctx, req)
	if err != nil {
		return err
	}
	if push {
		return runtime.Push.Revalidate(ctx, req, receipt)
	}
	observed, err := runtime.observe(ctx, req, authority, pair)
	if err != nil {
		return err
	}
	// A configured or stored source is never bound to an arranged pull, so a
	// receipt that carries one belongs to a different physical decision.
	if receipt.Pull != nil {
		return status.Error(codes.FailedPrecondition, "configured source placement has a different physical binding")
	}
	if receipt.Response != nil && (receipt.Response.GetReady() || receipt.Response.GetEndpoint() != observed.endpoint ||
		receipt.Response.GetPublicBaseUrl() != observed.node.Host || receipt.Response.GetExpiresAt().AsTime().After(observed.expiresAt)) {
		return status.Error(codes.FailedPrecondition, "prepared playback path changed")
	}
	return nil
}

func (runtime *MediaServePreparationRuntime) Reconcile(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
	authority, pair, push, err := runtime.classify(ctx, req)
	if err != nil {
		return nil, err
	}
	if push {
		return runtime.Push.Reconcile(ctx, req, receipt, bind)
	}
	if receipt.Pull != nil {
		return nil, status.Error(codes.FailedPrecondition, "configured source placement has a different physical binding")
	}
	observed, err := runtime.observe(ctx, req, authority, pair)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if !runtime.now().Before(observed.expiresAt) {
		return nil, status.Error(codes.FailedPrecondition, "media preparation evidence expired")
	}
	q := req.Query
	return &placementpb.Preparation{
		Outcome:  placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED,
		TenantId: q.TenantId, ObjectId: q.ObjectId, SourceGeneration: q.SourceGeneration,
		ClusterId: req.ClusterId, NodeId: req.NodeId, Protocol: q.Protocol,
		PolicyRevision: q.PolicyRevision, ParentRevision: q.ParentRevision, PolicyDigest: q.PolicyDigest,
		AttemptId: req.AttemptId, Endpoint: observed.endpoint, PublicBaseUrl: observed.node.Host, ExpiresAt: timestamppb.New(observed.expiresAt),
	}, nil
}

// classify reads the signed pair once and reports whether the push runtime owns
// this preparation. Dispatch comes from signed object kind and ingest mode only.
func (runtime *MediaServePreparationRuntime) classify(ctx context.Context, req *placementpb.PreparePlacementRequest) (balancer.PlacementAuthority, localauthority.PlacementPair, bool, error) {
	if runtime == nil || runtime.CellID == "" || runtime.Authority == nil || runtime.Paths == nil || runtime.Push == nil || runtime.Snapshot == nil {
		return balancer.PlacementAuthority{}, localauthority.PlacementPair{}, false, status.Error(codes.Unavailable, "media preparation is unavailable")
	}
	if err := placement.ValidatePreparationDeadline(req, runtime.now()); err != nil || req.GetQuery().GetVerb() != placementpb.Verb_VERB_SERVE {
		return balancer.PlacementAuthority{}, localauthority.PlacementPair{}, false, status.Error(codes.InvalidArgument, "media preparation requires a valid serving request")
	}
	readCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	q := req.Query
	pair, err := runtime.Authority.Placement(readCtx, q.TenantId, q.ObjectId, q.InternalName)
	if err != nil {
		return balancer.PlacementAuthority{}, localauthority.PlacementPair{}, false, err
	}
	if err = readCtx.Err(); err != nil {
		return balancer.PlacementAuthority{}, localauthority.PlacementPair{}, false, status.FromContextError(err).Err()
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Serve, runtime.now())
	if err != nil {
		return balancer.PlacementAuthority{}, localauthority.PlacementPair{}, false, err
	}
	if !authority.MatchesQuery(q, runtime.CellID) {
		return balancer.PlacementAuthority{}, localauthority.PlacementPair{}, false, status.Error(codes.FailedPrecondition, "media preparation authority does not match")
	}
	push := authority.ObjectKind == mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM && authority.IngestMode == "push"
	return authority, pair, push, nil
}

// observe reuses the kind's own discovery evidence for the single selected
// destination, so preparation cannot accept a node discovery would have refused.
func (runtime *MediaServePreparationRuntime) observe(ctx context.Context, req *placementpb.PreparePlacementRequest, authority balancer.PlacementAuthority, pair localauthority.PlacementPair) (mediaPreparationState, error) {
	now := runtime.now()
	snapshot := runtime.Snapshot()
	if snapshot == nil || len(snapshot.Nodes) > 4096 {
		return mediaPreparationState{}, status.Error(codes.Unavailable, "destination telemetry is unavailable")
	}
	var selected *state.EnhancedBalancerNodeSnapshot
	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if node.NodeID != req.NodeId {
			continue
		}
		if selected != nil || node.ClusterID != req.ClusterId {
			return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "destination identity is ambiguous")
		}
		selected = node
	}
	if selected == nil || !selected.IsActive || !selected.CapEdge || !freshPlacementEvidence(selected.LastHeartbeat, now) || !freshPlacementEvidence(selected.OutputsObservedAt, now) {
		return mediaPreparationState{}, status.Error(codes.Unavailable, "selected destination is unavailable")
	}
	if _, allowed := authority.Clusters[req.ClusterId]; !allowed {
		return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "selected destination is outside authority")
	}
	endpoint := mist.ResolvePlaybackURL(selected.Outputs, selected.Host, req.Query.Protocol, pair.Object.Authority.GetPlaybackId())
	if endpoint == "" {
		return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "selected playback endpoint is unavailable")
	}
	observation, err := runtime.Paths.ObservePlacementPaths(ctx, authority, req.Query, &state.BalancerSnapshot{Timestamp: now, Nodes: []state.EnhancedBalancerNodeSnapshot{*selected}})
	if err != nil {
		return mediaPreparationState{}, status.Error(codes.Unavailable, "selected destination source evidence is unavailable")
	}
	// The reader stamps its own observation time, which is necessarily later than
	// the clock read that selected the node. Compare freshness against a reading
	// taken after the observation, so evidence gathered during this call is not
	// mistaken for evidence from the future.
	observedNow := runtime.now()
	path, known := observation.Paths[req.NodeId]
	if !known || observation.SourceGeneration != req.Query.SourceGeneration ||
		observation.ObservedAt.IsZero() || observation.ObservedAt.After(observedNow) || !observedNow.Before(observation.ExpiresAt) {
		return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "selected destination source evidence is stale")
	}
	if path.Presence != placement.Present && !path.SourceFeasible {
		return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "selected destination cannot use this source")
	}
	if err := ctx.Err(); err != nil {
		return mediaPreparationState{}, status.FromContextError(err).Err()
	}
	expiresAt := minPlacementExpiry(req.ExpiresAt.AsTime(), authority.ExpiresAt)
	for _, expiry := range []time.Time{observation.ExpiresAt, selected.LastHeartbeat.Add(30 * time.Second), selected.OutputsObservedAt.Add(30 * time.Second)} {
		expiresAt = minPlacementExpiry(expiresAt, expiry)
	}
	return mediaPreparationState{node: *selected, endpoint: endpoint, expiresAt: expiresAt}, nil
}
