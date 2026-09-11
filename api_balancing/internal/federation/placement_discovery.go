package federation

import (
	"context"
	"errors"
	"slices"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type PlacementAuthorityReader interface {
	Placement(context.Context, string, string, string) (localauthority.PlacementPair, error)
}

type PlacementInventoryReader interface {
	GetMediaPlacementInventory(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error)
}

// PlacementPathObservation is independently resolved, credential-free evidence
// for one generation and exact destination inventory. It does not reserve or
// start a source. Missing paths remain unavailable candidates.
type PlacementPathObservation struct {
	Paths            map[string]balancer.PlacementNodePath
	SourceGeneration string
	ObservedAt       time.Time
	ExpiresAt        time.Time
}

type PlacementPathReader interface {
	ObservePlacementPaths(context.Context, balancer.PlacementAuthority, *placementpb.CandidateQuery, *state.BalancerSnapshot) (PlacementPathObservation, error)
}

// PlacementDiscovery composes the same destination-owned observation for local
// and federated callers. It neither ranks nor truncates nodes by proximity or
// presence. Public callers must enter through authenticated front doors.
type PlacementDiscovery struct {
	CellID    string
	Authority PlacementAuthorityReader
	Inventory PlacementInventoryReader
	Paths     PlacementPathReader
	Snapshot  func() *state.BalancerSnapshot
	Now       func() time.Time
}

func (discovery *PlacementDiscovery) QueryPlacementCandidates(ctx context.Context, req *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
	if err := placement.ValidateCandidateQuery(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid placement query")
	}
	if discovery == nil || discovery.CellID == "" || discovery.Authority == nil || discovery.Inventory == nil || discovery.Paths == nil || discovery.Snapshot == nil {
		return nil, status.Error(codes.Unavailable, "placement discovery is not ready")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	pair, err := discovery.Authority.Placement(ctx, req.GetTenantId(), req.GetObjectId(), req.GetInternalName())
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement authority is unavailable")
	}
	verb := placement.Serve
	if req.GetVerb() == placementpb.Verb_VERB_INGEST {
		verb = placement.Ingest
	}
	authority, err := balancer.CompilePlacementAuthority(pair, verb, discovery.now())
	if errors.Is(err, balancer.ErrPlacementAuthorityDenied) {
		return nil, status.Error(codes.PermissionDenied, "placement authority denies discovery")
	}
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "placement authority is not ready")
	}
	if !authority.MatchesQuery(req, discovery.CellID) {
		return nil, status.Error(codes.FailedPrecondition, "placement authority does not match query")
	}
	cell := balancer.PlacementCell{ID: discovery.CellID, ClusterIDs: slices.Clone(req.GetClusterIds())}
	membership, err := discovery.Inventory.GetMediaPlacementInventory(ctx, &quartermasterpb.GetMediaPlacementInventoryRequest{
		TenantId: authority.TenantID, ControlCellId: cell.ID, ClusterIds: slices.Clone(cell.ClusterIDs),
	})
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement inventory is unavailable")
	}
	joined, err := balancer.ReconcilePlacementInventory(authority.TenantID, cell, membership, discovery.Snapshot(), discovery.now())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement inventory is inconsistent")
	}
	paths, err := discovery.Paths.ObservePlacementPaths(ctx, authority, req, joined.Snapshot)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement paths are unavailable")
	}
	now := discovery.now()
	if paths.SourceGeneration != req.GetSourceGeneration() || paths.ObservedAt.IsZero() || paths.ObservedAt.After(now) ||
		!now.Before(paths.ExpiresAt) || paths.ExpiresAt.After(paths.ObservedAt.Add(30*time.Second)) || !now.Before(authority.ExpiresAt) {
		return nil, status.Error(codes.Unavailable, "placement paths are stale or inconsistent")
	}
	if len(paths.Paths) > len(joined.Snapshot.Nodes) {
		return nil, status.Error(codes.Unavailable, "placement paths exceed inventory")
	}
	members := make(map[string]bool, len(joined.Snapshot.Nodes))
	for _, node := range joined.Snapshot.Nodes {
		members[node.NodeID] = true
	}
	for nodeID := range paths.Paths {
		if !members[nodeID] {
			return nil, status.Error(codes.Unavailable, "placement path is outside inventory")
		}
	}
	clusters := make(map[string]balancer.PlacementClusterFacts, len(cell.ClusterIDs))
	for _, clusterID := range cell.ClusterIDs {
		clusters[clusterID] = authority.Clusters[clusterID]
	}
	observation, err := joined.Observe(balancer.PlacementObservationRequest{
		TenantID: authority.TenantID, Verb: authority.Verb, InternalName: authority.InternalName, Protocol: req.GetProtocol(),
		Now: now, Clusters: clusters, Paths: paths.Paths,
	})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "placement observation is unavailable")
	}
	response := &placementpb.CandidateObservation{
		Complete: observation.Complete, PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision,
		ParentRevision: authority.ParentRevision, SourceGeneration: paths.SourceGeneration,
		ObservedAt: timestamppb.New(now),
	}
	expiresAt := observation.ExpiresAt
	for _, expiry := range []time.Time{paths.ExpiresAt, authority.ExpiresAt} {
		if expiry.Before(expiresAt) {
			expiresAt = expiry
		}
	}
	response.ExpiresAt = timestamppb.New(expiresAt)
	for _, candidate := range observation.Candidates {
		if paths.ExpiresAt.Before(candidate.ExpiresAt) {
			candidate.ExpiresAt = paths.ExpiresAt
		}
		wire, encodeErr := placement.CandidateToProto(candidate, authority.Verb)
		if encodeErr != nil {
			return nil, status.Error(codes.Unavailable, "placement observation is inconsistent")
		}
		response.Candidates = append(response.Candidates, wire)
	}
	return response, nil
}

func (discovery *PlacementDiscovery) now() time.Time {
	if discovery.Now != nil {
		return discovery.Now().UTC()
	}
	return time.Now().UTC()
}
