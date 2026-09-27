package federation

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PlacementAdmissionInput is supplied by authenticated final-admission handlers.
// Node/cluster identity comes from the admitted edge, geography from its trusted
// client address, and source generation from the source owner. Client URLs cannot
// supply policy revisions, a grant census, or a preparation receipt as permission.
type PlacementAdmissionInput struct {
	TenantID, ObjectID, InternalName string
	ClusterID, NodeID, Protocol      string
	SourceGeneration                 string
	Verb                             placement.Verb
	Location                         *placement.Coordinates
	// Optional paired versions bind owner-resolved source evidence to authority.
	TenantAuthorityVersion, ObjectAuthorityVersion int64
}

// PlacementAdmissionDecision is a current exact-destination policy check, not a
// reservation, publisher ownership claim, or physical first-media attestation.
// Callers must complete those independent checks and consume this before expiry.
type PlacementAdmissionDecision struct {
	TenantID, ObjectID, InternalName               string
	ClusterID, NodeID, Protocol                    string
	SourceGeneration, PolicyDigest                 string
	PolicyRevision, ParentRevision                 uint64
	TenantAuthorityVersion, ObjectAuthorityVersion int64
	Verb                                           placement.Verb
	ExpiresAt                                      time.Time
}

// Admit reconstructs policy and the complete cell census from signed authority.
// It reuses global preference revalidation without starting media or trusting a
// previously returned endpoint, which also permits policy checks for direct URLs.
func (gate *PlacementPolicyGate) Admit(ctx context.Context, input PlacementAdmissionInput) (PlacementAdmissionDecision, error) {
	if gate == nil || gate.CellID == "" || gate.Authority == nil || gate.Router.Observe == nil {
		return PlacementAdmissionDecision{}, status.Error(codes.Unavailable, "placement admission is unavailable")
	}
	for _, value := range []string{input.TenantID, input.ObjectID, input.InternalName, input.ClusterID, input.NodeID, input.Protocol} {
		if value == "" || len(value) > 255 || strings.TrimSpace(value) != value {
			return PlacementAdmissionDecision{}, status.Error(codes.InvalidArgument, "placement admission identity is required")
		}
	}
	if (input.Verb != placement.Ingest && input.Verb != placement.Serve) || (input.Verb == placement.Serve && input.SourceGeneration == "") {
		return PlacementAdmissionDecision{}, status.Error(codes.InvalidArgument, "placement admission verb or source is invalid")
	}
	if input.TenantAuthorityVersion < 0 || input.ObjectAuthorityVersion < 0 || (input.TenantAuthorityVersion == 0) != (input.ObjectAuthorityVersion == 0) {
		return PlacementAdmissionDecision{}, status.Error(codes.InvalidArgument, "placement admission authority binding is incomplete")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return PlacementAdmissionDecision{}, status.FromContextError(err).Err()
	}
	authority, err := gate.readAdmissionAuthority(ctx, input)
	if err != nil {
		return PlacementAdmissionDecision{}, err
	}
	// Assessment reads authority again. When it no longer matches the snapshot
	// this admission was built from, the admission is re-made on the newer
	// snapshot; only a policy that forbids the destination refuses it.
	for round := 1; ; round++ {
		decision, err := gate.admitOn(ctx, input, authority)
		if !errors.Is(err, errPlacementAuthorityMismatch) {
			return decision, err
		}
		current, readErr := gate.readAdmissionAuthority(ctx, input)
		if readErr != nil {
			return PlacementAdmissionDecision{}, readErr
		}
		if placementDecisionFactsEqual(authority, current) {
			// The mismatch is not a change: this cell's grants do not cover it.
			return PlacementAdmissionDecision{}, err
		}
		if round == placementAuthorityRounds {
			return PlacementAdmissionDecision{}, &placementAuthorityChangedError{rounds: round}
		}
		authority = current
	}
}

func (gate *PlacementPolicyGate) readAdmissionAuthority(ctx context.Context, input PlacementAdmissionInput) (balancer.PlacementAuthority, error) {
	readCtx, stopRead := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	pair, err := gate.Authority.Placement(readCtx, input.TenantID, input.ObjectID, input.InternalName)
	readErr := readCtx.Err()
	stopRead()
	if readErr != nil {
		return balancer.PlacementAuthority{}, status.FromContextError(readErr).Err()
	}
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	authority, err := balancer.CompilePlacementAuthority(pair, input.Verb, gate.now())
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	if authority.TenantID != input.TenantID || authority.ObjectID != input.ObjectID || authority.InternalName != input.InternalName {
		return balancer.PlacementAuthority{}, status.Error(codes.FailedPrecondition, "placement admission authority identity differs")
	}
	return authority, nil
}

// admitOn assesses the admission under one authority snapshot. Owner-resolved
// source evidence is bound to exact authority versions, so a bound admission
// still refuses a snapshot whose versions differ.
func (gate *PlacementPolicyGate) admitOn(ctx context.Context, input PlacementAdmissionInput, authority balancer.PlacementAuthority) (PlacementAdmissionDecision, error) {
	if input.TenantAuthorityVersion > 0 && (input.TenantAuthorityVersion != authority.TenantAuthorityVersion || input.ObjectAuthorityVersion != authority.ObjectAuthorityVersion) {
		return PlacementAdmissionDecision{}, status.Error(codes.FailedPrecondition, "placement source evidence belongs to different authority")
	}
	query := &pb.CandidateQuery{TenantId: input.TenantID, ObjectId: input.ObjectID, InternalName: input.InternalName,
		Verb: pb.Verb_VERB_SERVE, Protocol: input.Protocol, SourceGeneration: input.SourceGeneration,
		PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision, ParentRevision: authority.ParentRevision}
	if input.Verb == placement.Ingest {
		query.Verb = pb.Verb_VERB_INGEST
	}
	if input.Location != nil {
		query.ClientLocation = &pb.Coordinates{Latitude: input.Location.Latitude, Longitude: input.Location.Longitude}
	}
	for _, cell := range authority.Cells {
		if cell.ID == gate.CellID {
			query.ClusterIds = slices.Clone(cell.ClusterIDs)
			break
		}
	}
	if !slices.Contains(query.ClusterIds, input.ClusterID) {
		return PlacementAdmissionDecision{}, status.Error(codes.PermissionDenied, "admission destination is outside this cell's grants")
	}
	now := gate.now()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		return PlacementAdmissionDecision{}, err
	}
	until := now.Truncate(time.Millisecond).Add(placement.PreparationLifetime)
	if authority.ExpiresAt.Before(until) {
		until = authority.ExpiresAt
	}
	checked, err := gate.Validate(ctx, &pb.PreparePlacementRequest{Query: query, ClusterId: input.ClusterID, NodeId: input.NodeID,
		AttemptId: attempt, ExpiresAt: timestamppb.New(until)})
	if err != nil {
		return PlacementAdmissionDecision{}, err
	}
	// Assessment decided on its own reads, which matched this request's policy
	// and may carry newer versions of the same facts; the decision carries them.
	if input.TenantAuthorityVersion > 0 && (checked.TenantAuthorityVersion != input.TenantAuthorityVersion || checked.ObjectAuthorityVersion != input.ObjectAuthorityVersion) {
		return PlacementAdmissionDecision{}, status.Error(codes.FailedPrecondition, "placement source evidence belongs to different authority")
	}
	return PlacementAdmissionDecision{TenantID: input.TenantID, ObjectID: input.ObjectID, InternalName: input.InternalName,
		ClusterID: input.ClusterID, NodeID: input.NodeID, Protocol: input.Protocol, SourceGeneration: input.SourceGeneration,
		PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision, ParentRevision: authority.ParentRevision,
		TenantAuthorityVersion: checked.TenantAuthorityVersion, ObjectAuthorityVersion: checked.ObjectAuthorityVersion,
		Verb: input.Verb, ExpiresAt: checked.ExpiresAt}, nil
}
