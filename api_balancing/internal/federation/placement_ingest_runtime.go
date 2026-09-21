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

// LiveIngestPreparationRuntime confirms one push-ingest listener without a
// publishing credential or an ownership claim. PolicyBoundPlacementRuntime
// supplies global policy, capacity and active-ingest-fence checks.
type LiveIngestPreparationRuntime struct {
	CellID    string
	Authority PlacementAuthorityReader
	Snapshot  func() *state.BalancerSnapshot
	Now       func() time.Time
}

var _ PlacementPreparationRuntime = (*LiveIngestPreparationRuntime)(nil)

type ingestListener struct {
	endpoint, publicBaseURL string
	expiresAt               time.Time
}

func (runtime *LiveIngestPreparationRuntime) Revalidate(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	if receipt.Pull != nil {
		return status.Error(codes.FailedPrecondition, "ingest preparation cannot carry a source pull")
	}
	listener, err := runtime.observe(ctx, req)
	if err != nil {
		return err
	}
	if receipt.Response != nil && (receipt.Response.GetReady() || receipt.Response.GetEndpoint() != listener.endpoint || receipt.Response.GetPublicBaseUrl() != listener.publicBaseURL || receipt.Response.GetExpiresAt().AsTime().After(listener.expiresAt)) {
		return status.Error(codes.FailedPrecondition, "prepared ingest listener changed")
	}
	return nil
}

func (runtime *LiveIngestPreparationRuntime) Reconcile(ctx context.Context, req *placementpb.PreparePlacementRequest, receipt PlacementReceipt, _ func(*PlacementPullBinding) error) (*placementpb.Preparation, error) {
	if receipt.Pull != nil {
		return nil, status.Error(codes.FailedPrecondition, "ingest preparation cannot carry a source pull")
	}
	listener, err := runtime.observe(ctx, req)
	if err != nil {
		return nil, err
	}
	q := req.Query
	return &placementpb.Preparation{Outcome: placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED,
		TenantId: q.TenantId, ObjectId: q.ObjectId, ClusterId: req.ClusterId, NodeId: req.NodeId,
		Protocol: q.Protocol, PolicyRevision: q.PolicyRevision, ParentRevision: q.ParentRevision,
		PolicyDigest: q.PolicyDigest, AttemptId: req.AttemptId, Endpoint: listener.endpoint, PublicBaseUrl: listener.publicBaseURL, ExpiresAt: timestamppb.New(listener.expiresAt)}, nil
}

func (runtime *LiveIngestPreparationRuntime) observe(ctx context.Context, req *placementpb.PreparePlacementRequest) (ingestListener, error) {
	if runtime == nil || runtime.CellID == "" || runtime.Authority == nil || runtime.Snapshot == nil {
		return ingestListener{}, status.Error(codes.Unavailable, "ingest preparation observations are unavailable")
	}
	if err := placement.ValidatePreparationDeadline(req, runtime.now()); err != nil || req.GetQuery().GetVerb() != placementpb.Verb_VERB_INGEST || req.GetQuery().GetSourceGeneration() != "" {
		return ingestListener{}, status.Error(codes.InvalidArgument, "ingest preparation requires a valid publisher request")
	}
	q := req.Query
	readCtx, stopRead := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	pair, err := runtime.Authority.Placement(readCtx, q.TenantId, q.ObjectId, q.InternalName)
	stopRead()
	if err != nil {
		return ingestListener{}, err
	}
	now := runtime.now()
	authority, err := balancer.CompilePlacementAuthority(pair, placement.Ingest, now)
	if err != nil {
		return ingestListener{}, err
	}
	if !authority.MatchesQuery(q, runtime.CellID) || authority.ObjectKind != mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM || authority.IngestMode != "push" {
		return ingestListener{}, status.Error(codes.FailedPrecondition, "ingest preparation authority does not match")
	}
	if _, allowed := authority.Clusters[req.ClusterId]; !allowed {
		return ingestListener{}, status.Error(codes.PermissionDenied, "ingest destination is outside authority")
	}
	snapshot := runtime.Snapshot()
	if snapshot == nil || len(snapshot.Nodes) > 4096 {
		return ingestListener{}, status.Error(codes.Unavailable, "ingest telemetry is unavailable")
	}
	var selected *state.EnhancedBalancerNodeSnapshot
	for i := range snapshot.Nodes {
		node := &snapshot.Nodes[i]
		if node.NodeID == req.NodeId {
			if selected != nil || node.ClusterID != req.ClusterId {
				return ingestListener{}, status.Error(codes.FailedPrecondition, "ingest destination identity is ambiguous")
			}
			selected = node
		}
	}
	if selected == nil || !selected.IsActive || !selected.CapIngest || !freshPlacementEvidence(selected.LastHeartbeat, now) || !freshPlacementEvidence(selected.OutputsObservedAt, now) {
		return ingestListener{}, status.Error(codes.Unavailable, "selected ingest destination is unavailable")
	}
	endpoint := mist.ResolveIngestEndpointTemplate(selected.Outputs, selected.Host, q.Protocol)
	publicBase := mist.IngestPublicOrigin(selected.Host)
	if endpoint == "" || publicBase == "" {
		return ingestListener{}, status.Error(codes.FailedPrecondition, "selected ingest listener is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return ingestListener{}, status.FromContextError(err).Err()
	}
	expiresAt := minPlacementExpiry(req.ExpiresAt.AsTime(), authority.ExpiresAt)
	expiresAt = minPlacementExpiry(expiresAt, selected.LastHeartbeat.Add(30*time.Second))
	expiresAt = minPlacementExpiry(expiresAt, selected.OutputsObservedAt.Add(30*time.Second))
	if !runtime.now().Before(expiresAt) {
		return ingestListener{}, status.Error(codes.FailedPrecondition, "ingest preparation evidence expired")
	}
	return ingestListener{endpoint: endpoint, publicBaseURL: publicBase, expiresAt: expiresAt}, nil
}

func (runtime *LiveIngestPreparationRuntime) now() time.Time {
	if runtime.Now != nil {
		return runtime.Now().UTC()
	}
	return time.Now().UTC()
}
