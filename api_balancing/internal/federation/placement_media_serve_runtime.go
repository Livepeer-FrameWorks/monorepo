package federation

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MediaServePreparationRuntime dispatches serving preparation by signed object
// kind. Push streams and configured inputs relayed from another cell bind their
// arranged origin pull. Configured inputs that the destination may originate and
// stored artifacts are materialized locally and therefore bind no pull.
type MediaServePreparationRuntime struct {
	CellID    string
	Authority PlacementAuthorityReader
	Paths     *MediaPlacementPaths
	Push      *LivePushPreparationRuntime
	Registry  *control.StreamRegistry
	Arrange   *ArrangeOriginPullDeps
	Snapshot  func() *state.BalancerSnapshot
	Now       func() time.Time
}

var _ PlacementPreparationRuntime = (*MediaServePreparationRuntime)(nil)

type mediaPreparationState struct {
	node                  state.EnhancedBalancerNodeSnapshot
	endpoint              string
	expiresAt             time.Time
	configuredRelay       *configuredSource
	configuredGeneration  string
	configuredRevision    int64
	configuredDestination int64
}

// PushPreparation returns the push half of a configured destination's serving
// runtime. A destination that startup has not configured, or one whose serving
// runtime is not this dispatcher, returns nil so the caller refuses instead of
// guessing.
func (destination *PlacementDestination) PushPreparation() *LivePushPreparationRuntime {
	serve := destination.MediaServePreparation()
	if serve == nil || serve.Push == nil || serve.Push.Paths != serve.Paths.Push {
		return nil
	}
	return serve.Push
}

// MediaServePreparation returns the signed-kind dispatcher installed on a
// placement destination. Final source admission uses the same dispatcher so a
// configured relay is never checked as though it were a push publisher.
func (destination *PlacementDestination) MediaServePreparation() *MediaServePreparationRuntime {
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
	if !ok || serve == nil || serve.Paths == nil {
		return nil
	}
	if destination.Discovery == nil || PlacementPathReader(serve.Paths) != destination.Discovery.Paths {
		return nil
	}
	return serve
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
	if observed.configuredRelay == nil {
		if receipt.Pull != nil {
			return status.Error(codes.FailedPrecondition, "local media placement has a different physical binding")
		}
	} else {
		if receipt.Pull != nil && !configuredPullMatchesSource(receipt.Pull, observed) {
			return status.Error(codes.FailedPrecondition, "configured source or destination connection changed")
		}
		// A fresh receipt has no pull until Reconcile binds one. A completed
		// response, however, must still have the exact current physical pull.
		if receipt.Response == nil {
			return nil
		}
		if runtime.Registry == nil || receipt.Pull == nil {
			return status.Error(codes.Unavailable, "configured source pull binding is unavailable")
		}
		pull, found, pullErr := runtime.Registry.CurrentInboundPull(ctx, req.Query.InternalName, req.NodeId)
		if pullErr != nil {
			return pullErr
		}
		if !found || pull.TenantID != req.Query.TenantId || pull.DestClusterID != req.ClusterId || pull.DestNodeID != req.NodeId ||
			pull.AttemptID != receipt.Pull.AttemptID || pull.SourceClusterID != receipt.Pull.SourceCellID ||
			pull.SourceMediaClusterID != receipt.Pull.SourceClusterID || pull.SourceNodeID != receipt.Pull.SourceNodeID ||
			pull.SourceGeneration != receipt.Pull.SourceGeneration || pull.SourceRevision != receipt.Pull.SourceRevision ||
			control.SourcePullBaseURL(pull.DTSCURL) != observed.configuredRelay.dtscURL {
			return status.Error(codes.FailedPrecondition, "configured source pull is no longer current")
		}
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
	observed, err := runtime.observe(ctx, req, authority, pair)
	if err != nil {
		return nil, err
	}
	if observed.configuredRelay == nil {
		if receipt.Pull != nil {
			return nil, status.Error(codes.FailedPrecondition, "local media placement has a different physical binding")
		}
	} else {
		if runtime.Registry == nil || runtime.Arrange == nil || runtime.Arrange.Registry != runtime.Registry || bind == nil {
			return nil, status.Error(codes.Unavailable, "configured source preparation dependencies are unavailable")
		}
		source := observed.configuredRelay
		arrange := ArrangeOriginPullRequest{
			TenantID: req.Query.TenantId, InternalName: req.Query.InternalName,
			DestClusterID: req.ClusterId, DestNodeID: req.NodeId, DestNodeBaseURL: observed.node.Host,
			RemoteCluster: source.cellID, SourceGeneration: observed.configuredGeneration, SourceRevision: observed.configuredRevision,
			Remote: &federationpb.EdgeCandidate{ClusterId: source.clusterID, NodeId: source.nodeID,
				SourceGeneration: observed.configuredGeneration, SourceRevision: observed.configuredRevision},
			BindPull: bind, DestinationFence: observed.configuredDestination, AllowEquivalentSourceReplacement: true,
		}
		if receipt.Pull != nil {
			if !configuredPullMatchesSource(receipt.Pull, observed) {
				return nil, status.Error(codes.FailedPrecondition, "pending configured pull belongs to another source")
			}
			arrange.AttemptID = receipt.Pull.AttemptID
		}
		prepared, arrangeErr := runtime.Arrange.ArrangeOriginPull(ctx, arrange)
		if arrangeErr != nil {
			return nil, arrangeErr
		}
		if prepared == nil || prepared.DestNodeID != req.NodeId || prepared.SourceGeneration != observed.configuredGeneration ||
			prepared.SourceRevision != observed.configuredRevision || control.SourcePullBaseURL(prepared.PullDTSCURL) != source.dtscURL {
			return nil, status.Error(codes.FailedPrecondition, "configured source preparation returned an invalid path")
		}
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
	state := mediaPreparationState{node: *selected, endpoint: endpoint}
	if authority.ObjectKind == mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM && ConfiguredIngestMode(authority.IngestMode) {
		configured := runtime.Paths.Configured
		if configured == nil || authority.ObjectAuthorityVersion <= 0 {
			return mediaPreparationState{}, status.Error(codes.Unavailable, "configured source preparation is unavailable")
		}
		descriptor, dial, describeErr := configured.describe(ctx, authority)
		if describeErr != nil || descriptor.Generation != req.Query.SourceGeneration {
			return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "configured source generation changed")
		}
		if path.Presence != placement.Present && !configured.mayOriginate(descriptor, authority, dial, selected.ClusterID, selected.NodeID, configured.now()) {
			if runtime.Registry == nil || runtime.Arrange == nil {
				return mediaPreparationState{}, status.Error(codes.Unavailable, "configured relay preparation is unavailable")
			}
			source, sourceErr := configured.liveSource(ctx, descriptor, authority, dial)
			if sourceErr != nil {
				return mediaPreparationState{}, sourceErr
			}
			if source == nil || source.dtscURL == "" {
				return mediaPreparationState{}, status.Error(codes.FailedPrecondition, "configured relay source is unavailable")
			}
			fence, fenceErr := runtime.Push.currentDestinationFence(ctx, req.NodeId, req.ClusterId)
			if fenceErr != nil {
				return mediaPreparationState{}, fenceErr
			}
			state.configuredRelay = source
			state.configuredGeneration = descriptor.Generation
			state.configuredRevision = authority.ObjectAuthorityVersion
			state.configuredDestination = fence
		}
	}
	if err := ctx.Err(); err != nil {
		return mediaPreparationState{}, status.FromContextError(err).Err()
	}
	expiresAt := minPlacementExpiry(req.ExpiresAt.AsTime(), authority.ExpiresAt)
	for _, expiry := range []time.Time{observation.ExpiresAt, selected.LastHeartbeat.Add(30 * time.Second), selected.OutputsObservedAt.Add(30 * time.Second)} {
		expiresAt = minPlacementExpiry(expiresAt, expiry)
	}
	state.expiresAt = expiresAt
	return state, nil
}

func configuredPullMatchesSource(pull *PlacementPullBinding, observed mediaPreparationState) bool {
	source := observed.configuredRelay
	return pull != nil && source != nil && canonicalPullAttempt(pull.AttemptID) &&
		pull.DestinationFence == observed.configuredDestination && pull.SourceCellID == source.cellID &&
		pull.SourceClusterID == source.clusterID && pull.SourceNodeID == source.nodeID &&
		pull.SourceGeneration == observed.configuredGeneration && pull.SourceRevision == observed.configuredRevision
}
