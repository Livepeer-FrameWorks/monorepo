package federation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
	// Logger receives clock-skew and membership drift warnings. Refusals are
	// returned with their reason in the status message and logged by the router.
	Logger logging.Logger
}

const placementDiscoveryMaxTimeout = 5 * time.Second

func (discovery *PlacementDiscovery) QueryPlacementCandidates(ctx context.Context, req *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
	if err := placement.ValidateCandidateQuery(req); err != nil {
		return nil, discovery.refuse(req, codes.InvalidArgument, "invalid placement query", err)
	}
	if discovery == nil || discovery.CellID == "" || discovery.Authority == nil || discovery.Inventory == nil || discovery.Paths == nil || discovery.Snapshot == nil {
		return nil, status.Error(codes.Unavailable, "placement discovery is not ready")
	}
	// Public and routing callers already carry a tighter end-to-end deadline.
	// Keep a ceiling for direct/internal callers, but do not replace the caller's
	// observation budget with a shorter dependency-local timer.
	ctx, cancel := context.WithTimeout(ctx, placementDiscoveryMaxTimeout)
	defer cancel()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	pair, err := discovery.Authority.Placement(ctx, req.GetTenantId(), req.GetObjectId(), req.GetInternalName())
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return nil, discovery.refuse(req, codes.Unavailable, "placement authority is unavailable", err)
	}
	verb := placement.Serve
	if req.GetVerb() == placementpb.Verb_VERB_INGEST {
		verb = placement.Ingest
	}
	authority, err := balancer.CompilePlacementAuthority(pair, verb, discovery.now())
	if errors.Is(err, balancer.ErrPlacementAuthorityDenied) {
		return nil, discovery.refuse(req, codes.PermissionDenied, "placement authority denies discovery", err)
	}
	if err != nil {
		return nil, discovery.refuse(req, codes.FailedPrecondition, "placement authority is not ready", err)
	}
	if !authority.MatchesQuery(req, discovery.CellID) {
		return nil, status.Error(codes.FailedPrecondition, "placement authority does not match query")
	}
	cell := balancer.PlacementCell{ID: discovery.CellID, ClusterIDs: slices.Clone(req.GetClusterIds())}
	// Membership lifetime is anchored to this local instant, taken before the
	// read, so Quartermaster's clock can never make fresh membership look stale.
	readAt := discovery.now()
	membership, err := discovery.Inventory.GetMediaPlacementInventory(ctx, &quartermasterpb.GetMediaPlacementInventoryRequest{
		TenantId: authority.TenantID, ControlCellId: cell.ID, ClusterIds: slices.Clone(cell.ClusterIDs),
	})
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return nil, discovery.refuse(req, codes.Unavailable, "placement inventory is unavailable", err)
	}
	joined, err := balancer.ReconcilePlacementInventory(authority.TenantID, cell, membership, discovery.Snapshot(), readAt)
	if err != nil {
		return nil, discovery.refuse(req, codes.Unavailable, "placement inventory rejected: "+inventoryReason(err), err)
	}
	discovery.reportInventoryDrift(authority.TenantID, joined)
	paths, err := discovery.Paths.ObservePlacementPaths(ctx, authority, req, joined.Snapshot)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return nil, discovery.refuse(req, codes.Unavailable, "placement paths are unavailable", err)
	}
	now := discovery.now()
	if reason, detail := stalePlacementPaths(paths, req.GetSourceGeneration(), authority.ExpiresAt, now); reason != "" {
		return nil, discovery.refuse(req, codes.Unavailable, "placement paths are stale or inconsistent: "+reason, errors.New(detail))
	}
	if len(paths.Paths) > len(joined.Snapshot.Nodes) {
		return nil, discovery.refuse(req, codes.Unavailable, "placement paths exceed inventory", fmt.Errorf("%d paths, %d members", len(paths.Paths), len(joined.Snapshot.Nodes)))
	}
	members := make(map[string]bool, len(joined.Snapshot.Nodes))
	for _, node := range joined.Snapshot.Nodes {
		members[node.NodeID] = true
	}
	for nodeID := range paths.Paths {
		if !members[nodeID] {
			return nil, discovery.refuse(req, codes.Unavailable, "placement path is outside inventory", fmt.Errorf("node %q", nodeID))
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
		return nil, discovery.refuse(req, codes.Unavailable, "placement observation is unavailable", err)
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
			return nil, discovery.refuse(req, codes.Unavailable, "placement observation is inconsistent", fmt.Errorf("candidate %s/%s: %w", candidate.ClusterID, candidate.NodeID, encodeErr))
		}
		response.Candidates = append(response.Candidates, wire)
	}
	return response, nil
}

// stalePlacementPaths returns a stable reason code and a detail line when path
// evidence cannot back an observation, or two empty strings when it can.
func stalePlacementPaths(paths PlacementPathObservation, generation string, authorityExpiresAt, now time.Time) (string, string) {
	switch {
	case paths.SourceGeneration != generation:
		return "generation_mismatch", "source generation " + paths.SourceGeneration + " does not match requested " + generation
	case paths.ObservedAt.IsZero():
		return "unobserved", "paths carry no observation time"
	case paths.ObservedAt.After(now):
		return "future_observation", "paths observed at " + paths.ObservedAt.Format(time.RFC3339Nano) + ", after now " + now.Format(time.RFC3339Nano)
	case !now.Before(paths.ExpiresAt):
		return "source_evidence_expired", "source evidence expired at " + paths.ExpiresAt.Format(time.RFC3339Nano) + ", now " + now.Format(time.RFC3339Nano)
	case paths.ExpiresAt.After(paths.ObservedAt.Add(30 * time.Second)):
		return "validity_too_long", "paths claim validity beyond 30s"
	case !now.Before(authorityExpiresAt):
		return "authority_expired", "placement authority expired at " + authorityExpiresAt.Format(time.RFC3339Nano)
	}
	return "", ""
}

// inventoryReason is the stable reason code of a membership refusal. It names
// the condition without dependency detail, so it is safe to return to peers.
func inventoryReason(err error) string {
	var typed *balancer.PlacementInventoryError
	if errors.As(err, &typed) {
		return typed.Reason
	}
	return "unknown"
}

// refuse logs a discovery refusal with its full cause and returns a status that
// carries only the stable message. Discovery also answers federation peers, so
// dependency detail stays in the local log.
func (discovery *PlacementDiscovery) refuse(req *placementpb.CandidateQuery, code codes.Code, message string, cause error) error {
	if discovery != nil && discovery.Logger != nil {
		discovery.Logger.WithError(cause).WithFields(logging.Fields{
			"cell_id": discovery.CellID, "tenant_id": req.GetTenantId(), "internal_name": req.GetInternalName(), "verb": req.GetVerb().String(), "code": code.String(),
		}).Warn("Placement discovery refused: " + message)
	}
	return status.Error(code, message)
}

// reportInventoryDrift logs clock skew above the coordinator skew budget and
// runtime nodes that membership does not list. Neither refuses placement.
func (discovery *PlacementDiscovery) reportInventoryDrift(tenantID string, joined *balancer.PlacementInventorySnapshot) {
	if discovery.Logger == nil || joined == nil {
		return
	}
	fields := logging.Fields{"cell_id": discovery.CellID, "tenant_id": tenantID}
	if joined.StampSkew > placement.PreparationClockSkew || joined.StampSkew < -placement.PreparationClockSkew {
		discovery.Logger.WithFields(fields).WithField("skew_ms", joined.StampSkew.Milliseconds()).
			Warn("Quartermaster membership stamp is skewed from the local clock; serving with local-clock lifetime")
	}
	if len(joined.UnregisteredNodes) > 0 {
		discovery.Logger.WithFields(fields).WithField("node_ids", joined.UnregisteredNodes).
			Warn("Runtime nodes are not listed in placement membership and cannot be selected")
	}
}

func (discovery *PlacementDiscovery) now() time.Time {
	if discovery.Now != nil {
		return discovery.Now().UTC()
	}
	return time.Now().UTC()
}
