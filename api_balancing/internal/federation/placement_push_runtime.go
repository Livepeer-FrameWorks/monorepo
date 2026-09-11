package federation

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
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

// LivePushPreparationRuntime prepares a tracked push-stream serving path.
// PolicyBoundPlacementRuntime supplies global admission. Arrangement publishes
// a tracked source for Mist's existing source resolver; it does not claim media
// readiness or start a separate pull for each viewer.
type LivePushPreparationRuntime struct {
	Authority        PlacementAuthorityReader
	Paths            *LivePushPlacementPaths
	Registry         *control.StreamRegistry
	Arrange          *ArrangeOriginPullDeps
	Now              func() time.Time
	DestinationFence func(context.Context, string, string) (int64, error)
}

var _ PlacementPreparationRuntime = (*LivePushPreparationRuntime)(nil)

type pushPreparationState struct {
	node             state.EnhancedBalancerNodeSnapshot
	source           *placementPublisher
	endpoint         string
	expiresAt        time.Time
	onPublisher      bool
	destinationFence int64
}

func (runtime *LivePushPreparationRuntime) Revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	observed, err := runtime.observe(ctx, req)
	if err != nil {
		return err
	}
	if receipt.Response != nil && (receipt.Response.GetReady() || receipt.Response.GetEndpoint() != observed.endpoint || receipt.Response.GetPublicBaseUrl() != observed.node.Host || receipt.Response.GetExpiresAt().AsTime().After(observed.expiresAt)) {
		return status.Error(codes.FailedPrecondition, "prepared playback path changed")
	}
	if observed.onPublisher {
		if receipt.Pull != nil {
			return status.Error(codes.FailedPrecondition, "publisher placement has a different physical binding")
		}
		return nil
	}
	if receipt.Pull != nil && (receipt.Pull.DestinationFence != observed.destinationFence || !pushPullMatchesSource(receipt.Pull, observed.source)) {
		return status.Error(codes.FailedPrecondition, "prepared source or destination connection changed")
	}
	if receipt.Response == nil {
		return nil
	}
	if receipt.Pull == nil || runtime.Registry == nil {
		return status.Error(codes.Unavailable, "prepared pull binding is unavailable")
	}
	pull, found, err := runtime.Registry.CurrentInboundPull(ctx, req.Query.InternalName, req.NodeId)
	if err != nil {
		return err
	}
	if !found || pull.TenantID != req.Query.TenantId || pull.DestClusterID != req.ClusterId || pull.AttemptID != receipt.Pull.AttemptID ||
		pull.SourceClusterID != receipt.Pull.SourceCellID || pull.SourceMediaClusterID != receipt.Pull.SourceClusterID || pull.SourceNodeID != receipt.Pull.SourceNodeID ||
		pull.SourceGeneration != receipt.Pull.SourceGeneration || pull.SourceRevision != receipt.Pull.SourceRevision ||
		control.SourcePullBaseURL(pull.DTSCURL) != observed.source.dtscURL ||
		!validPlacementDTSC(control.SourcePullBaseURL(pull.DTSCURL), control.RuntimeNameFor(control.IngestPush, req.Query.InternalName)) {
		return status.Error(codes.FailedPrecondition, "prepared pull is no longer current")
	}
	return nil
}

func (runtime *LivePushPreparationRuntime) Reconcile(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt, bind func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
	observed, err := runtime.observe(ctx, req)
	if err != nil {
		return nil, err
	}
	if observed.onPublisher {
		if receipt.Pull != nil {
			return nil, status.Error(codes.FailedPrecondition, "publisher placement has a different physical binding")
		}
	} else {
		if runtime.Registry == nil || runtime.Arrange == nil || runtime.Arrange.Registry != runtime.Registry || bind == nil {
			return nil, status.Error(codes.Unavailable, "push preparation dependencies are unavailable")
		}
		source := observed.source
		arrange := ArrangeOriginPullRequest{
			TenantID: req.Query.TenantId, InternalName: req.Query.InternalName,
			DestClusterID: req.ClusterId, DestNodeID: req.NodeId, DestNodeBaseURL: observed.node.Host,
			RemoteCluster: source.cellID, SourceGeneration: source.generation, SourceRevision: source.revision,
			Remote: &federationpb.EdgeCandidate{ClusterId: source.clusterID, NodeId: source.nodeID,
				SourceGeneration: source.generation, SourceRevision: source.revision},
			BindPull:         bind,
			DestinationFence: observed.destinationFence,
		}
		if receipt.Pull != nil {
			if receipt.Pull.DestinationFence != observed.destinationFence || !pushPullMatchesSource(receipt.Pull, source) {
				return nil, status.Error(codes.FailedPrecondition, "pending pull belongs to another source")
			}
			arrange.AttemptID = receipt.Pull.AttemptID
		}
		prepared, arrangeErr := runtime.Arrange.ArrangeOriginPull(ctx, arrange)
		if arrangeErr != nil {
			return nil, arrangeErr
		}
		if prepared == nil || prepared.DestNodeID != req.NodeId || !validPlacementDTSC(control.SourcePullBaseURL(prepared.PullDTSCURL), control.RuntimeNameFor(control.IngestPush, req.Query.InternalName)) {
			return nil, status.Error(codes.FailedPrecondition, "source preparation returned an invalid path")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if !runtime.now().Before(observed.expiresAt) {
		return nil, status.Error(codes.FailedPrecondition, "push preparation evidence expired")
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

func (runtime *LivePushPreparationRuntime) observe(ctx context.Context, req *placementpb.PreparePlacementRequest) (pushPreparationState, error) {
	if runtime == nil || runtime.Authority == nil || runtime.Paths == nil || runtime.Paths.CellID == "" || runtime.Paths.Registry == nil || runtime.Paths.Snapshot == nil {
		return pushPreparationState{}, status.Error(codes.Unavailable, "push preparation observations are unavailable")
	}
	if err := placement.ValidatePreparationDeadline(req, runtime.now()); err != nil || req.GetQuery().GetVerb() != placementpb.Verb_VERB_SERVE {
		return pushPreparationState{}, status.Error(codes.InvalidArgument, "push preparation requires a valid serving request")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	q := req.Query
	pair, err := runtime.Authority.Placement(ctx, q.TenantId, q.ObjectId, q.InternalName)
	if err != nil {
		return pushPreparationState{}, err
	}
	now := runtime.now()
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil {
		return pushPreparationState{}, err
	}
	if !authority.MatchesQuery(q, runtime.Paths.CellID) || authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM || authority.IngestMode != "push" {
		return pushPreparationState{}, status.Error(codes.FailedPrecondition, "push preparation authority does not match")
	}
	source, err := runtime.Paths.publisher(ctx, placementSourceContextFor(authority))
	if err != nil {
		return pushPreparationState{}, err
	}
	if source == nil || source.generation != q.SourceGeneration {
		return pushPreparationState{}, status.Error(codes.FailedPrecondition, "current publisher generation is unavailable")
	}
	snapshot := runtime.Paths.Snapshot()
	if snapshot == nil || len(snapshot.Nodes) > 4096 {
		return pushPreparationState{}, status.Error(codes.Unavailable, "destination telemetry is unavailable")
	}
	var selected *state.EnhancedBalancerNodeSnapshot
	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if node.NodeID == req.NodeId {
			if selected != nil || node.ClusterID != req.ClusterId {
				return pushPreparationState{}, status.Error(codes.FailedPrecondition, "destination identity is ambiguous")
			}
			selected = node
		}
	}
	if selected == nil || !selected.IsActive || !selected.CapEdge || !freshPlacementEvidence(selected.LastHeartbeat, now) || !freshPlacementEvidence(selected.OutputsObservedAt, now) {
		return pushPreparationState{}, status.Error(codes.Unavailable, "selected destination is unavailable")
	}
	endpoint := mist.ResolvePlaybackURL(selected.Outputs, selected.Host, q.Protocol, pair.Object.Authority.GetPlaybackId())
	if endpoint == "" {
		return pushPreparationState{}, status.Error(codes.FailedPrecondition, "selected playback endpoint is unavailable")
	}
	onPublisher := source.cellID == runtime.Paths.CellID && source.clusterID == req.ClusterId && source.nodeID == req.NodeId
	var destinationFence int64
	if !onPublisher {
		destinationFence, err = runtime.currentDestinationFence(ctx, req.NodeId, req.ClusterId)
		if err != nil {
			return pushPreparationState{}, err
		}
	}
	grant, allowed := authority.Clusters[req.ClusterId]
	if !allowed || (!onPublisher && (source.dtscURL == "" || (source.clusterID != req.ClusterId && !grant.AllowExternalSource))) {
		return pushPreparationState{}, status.Error(codes.FailedPrecondition, "selected destination cannot use this source")
	}
	if err := ctx.Err(); err != nil {
		return pushPreparationState{}, status.FromContextError(err).Err()
	}
	expiresAt := minPlacementExpiry(req.ExpiresAt.AsTime(), authority.ExpiresAt)
	for _, expiry := range []time.Time{source.expiresAt, selected.LastHeartbeat.Add(30 * time.Second), selected.OutputsObservedAt.Add(30 * time.Second)} {
		expiresAt = minPlacementExpiry(expiresAt, expiry)
	}
	return pushPreparationState{node: *selected, source: source, endpoint: endpoint, expiresAt: expiresAt, onPublisher: onPublisher, destinationFence: destinationFence}, nil
}

func pushPullMatchesSource(pull *PlacementPullBinding, source *placementPublisher) bool {
	return pull != nil && source != nil && canonicalPullAttempt(pull.AttemptID) && pull.SourceCellID == source.cellID &&
		pull.SourceClusterID == source.clusterID && pull.SourceNodeID == source.nodeID && pull.SourceGeneration == source.generation && pull.SourceRevision == source.revision
}

func (runtime *LivePushPreparationRuntime) currentDestinationFence(ctx context.Context, nodeID, clusterID string) (int64, error) {
	read := runtime.DestinationFence
	if read == nil {
		read = control.DestinationConnectionFence
	}
	fence, err := read(ctx, nodeID, clusterID)
	if err != nil || fence <= 0 {
		return 0, status.Error(codes.Unavailable, "destination connection ownership is unavailable")
	}
	return fence, nil
}

func (runtime *LivePushPreparationRuntime) now() time.Time {
	if runtime.Now != nil {
		return runtime.Now().UTC()
	}
	return time.Now().UTC()
}
