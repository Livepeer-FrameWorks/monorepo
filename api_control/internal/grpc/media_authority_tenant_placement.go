package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
)

func decodeTenantPlacementHistory(encoded []byte, tenantID string) (*mediapb.TenantAuthority, error) {
	previous := &mediapb.TenantAuthority{}
	if err := proto.Unmarshal(encoded, previous); err != nil {
		return nil, fmt.Errorf("decode tenant placement history: %w", err)
	}
	if previous.GetTenantId() != tenantID || (previous.GetSchemaVersion() != sharedauthority.SchemaVersion && previous.GetSchemaVersion() != sharedauthority.PlacementSchemaVersion) {
		return nil, errors.New("invalid tenant placement history identity or schema")
	}
	if previous.GetSchemaVersion() == sharedauthority.PlacementSchemaVersion && previous.GetMediaPlacement() == nil {
		return nil, errors.New("tenant placement history lacks policy")
	}
	if previous.GetSchemaVersion() == sharedauthority.SchemaVersion && previous.GetMediaPlacement() != nil {
		return nil, errors.New("legacy tenant history contains placement policy")
	}
	if err := placement.ValidatePolicySet(previous.GetMediaPlacement()); err != nil {
		return nil, err
	}
	return previous, nil
}

func tenantPlacementSourceRevisions(payload *mediapb.TenantAuthority, revisions []*mediapb.AuthoritySourceRevision) ([]*mediapb.AuthoritySourceRevision, error) {
	if payload.GetSchemaVersion() != sharedauthority.PlacementSchemaVersion {
		return revisions, nil
	}
	revision, err := hashProtoMessages(payload.GetMediaPlacement())
	if err != nil {
		return nil, err
	}
	out := append([]*mediapb.AuthoritySourceRevision(nil), revisions...)
	out = append(out, &mediapb.AuthoritySourceRevision{Service: "commodore", Revision: revision})
	sort.Slice(out, func(i, j int) bool { return out[i].GetService() < out[j].GetService() })
	return out, nil
}

// Established schema-2 history is inherited. A tenant still on the legacy
// schema, or without history, receives its first policy-bearing authority only
// when every current target cell has attested schema-2 enforcement; otherwise
// the refresh keeps the legacy schema, and the next attestation re-queues it.
func (s *CommodoreServer) compileTenantPlacement(ctx context.Context, payload *mediapb.TenantAuthority, entitlement *quartermasterpb.GetTenantEntitlementResponse, targets []string) error {
	queries := commodoredb.New(s.db)
	var previous *mediapb.TenantAuthority
	current, err := queries.GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "tenant", AuthorityID: payload.GetTenantId()})
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		if previous, err = decodeTenantPlacementHistory(current.Payload, payload.GetTenantId()); err != nil {
			return err
		}
	}
	established := previous.GetSchemaVersion() == sharedauthority.PlacementSchemaVersion
	active := payload.GetLifecycle() == mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && payload.GetBillingDecision() == mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW
	if !active {
		if established {
			return s.inheritTenantPlacement(ctx, payload, entitlement, previous)
		}
		return nil
	}
	if len(targets) == 0 {
		if established {
			return s.inheritTenantPlacement(ctx, payload, entitlement, previous)
		}
		return nil
	}
	ready, err := placementCellsReady(ctx, queries, targets)
	if err != nil {
		return err
	}
	var probeErr error
	if !ready {
		probeErr = s.refreshPlacementCellCapabilities(ctx, targets)
		ready, err = placementCellsReady(ctx, queries, targets)
		if err != nil {
			return err
		}
	}
	if !ready {
		if established {
			if probeErr != nil {
				return fmt.Errorf("placement enforcement capability unavailable: %w", probeErr)
			}
			return errors.New("tenant authority targets a cell without placement enforcement capability")
		}
		return nil
	}
	if established {
		return s.inheritTenantPlacement(ctx, payload, entitlement, previous)
	}
	// First issuance is opportunistic: a tenant whose owners have not yet
	// consented, or whose saved intent is incomplete, keeps its legacy refresh
	// instead of losing every refresh until that is fixed. Established schema-2
	// tenants keep the strict inheritance path above.
	candidate := proto.CloneOf(payload)
	if issueErr := s.issueTenantPlacement(ctx, candidate, entitlement, nil); issueErr != nil {
		if s.logger != nil {
			s.logger.WithError(issueErr).WithField("tenant_id", payload.GetTenantId()).Warn("Placement activation deferred; tenant keeps legacy authority schema")
		}
		return nil
	}
	payload.SchemaVersion, payload.MediaPlacement, payload.EffectiveClusterGrants = candidate.SchemaVersion, candidate.MediaPlacement, candidate.EffectiveClusterGrants
	return nil
}

func (s *CommodoreServer) inheritTenantPlacement(ctx context.Context, payload *mediapb.TenantAuthority, entitlement *quartermasterpb.GetTenantEntitlementResponse, previous *mediapb.TenantAuthority) error {
	if previous.GetSchemaVersion() == sharedauthority.SchemaVersion {
		return nil
	}
	if previous.GetSchemaVersion() != sharedauthority.PlacementSchemaVersion || previous.GetTenantId() != payload.GetTenantId() || previous.GetMediaPlacement() == nil {
		return errors.New("invalid tenant placement parent")
	}
	if payload.GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || payload.GetBillingDecision() != mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		// Revocation retains the revision fence without depending on mutable policy
		// rows or an available entitlement owner.
		if len(payload.GetEffectiveClusterGrants()) != 0 {
			return errors.New("denied tenant placement retained grants")
		}
		payload.SchemaVersion = sharedauthority.PlacementSchemaVersion
		payload.MediaPlacement = proto.CloneOf(previous.GetMediaPlacement())
		return nil
	}
	return s.issueTenantPlacement(ctx, payload, entitlement, previous.GetMediaPlacement())
}

// issueTenantPlacement compiles an active tenant's schema-2 payload from its
// saved policy intent and the owner entitlement. previousPolicy, when present,
// fences the saved intent against regression; nil means first issuance.
func (s *CommodoreServer) issueTenantPlacement(ctx context.Context, payload *mediapb.TenantAuthority, entitlement *quartermasterpb.GetTenantEntitlementResponse, previousPolicy *pb.PolicySet) error {
	snapshot, err := placementpolicy.NewStore(s.db).Read(ctx, placementpolicy.Scope{TenantID: payload.GetTenantId(), Kind: "tenant", ID: payload.GetTenantId()})
	if err != nil {
		return err
	}
	if previousPolicy != nil && (snapshot.Own.GetRevision() < previousPolicy.GetRevision() || snapshot.Own.GetRevision() == previousPolicy.GetRevision() && !proto.Equal(snapshot.Own, previousPolicy)) {
		return errors.New("tenant placement intent regressed or changed without revision")
	}
	if entitlement == nil || len(entitlement.GetEffectiveAccess()) > 4096 || len(entitlement.GetAllowedClusterIds()) > 4096 || proto.Size(entitlement) > 16<<20 || len(entitlement.ProtoReflect().GetUnknown()) != 0 {
		return errors.New("tenant placement entitlement is unavailable or oversized")
	}
	allowed := make(map[string]bool, len(entitlement.GetAllowedClusterIds()))
	for _, id := range entitlement.GetAllowedClusterIds() {
		if id == "" || allowed[id] {
			return errors.New("ambiguous tenant placement membership")
		}
		allowed[id] = true
	}
	peers := make(map[string]*clusterpb.TenantClusterPeer, len(entitlement.GetEffectiveAccess()))
	for _, peer := range entitlement.GetEffectiveAccess() {
		if peer.GetClusterId() == "" || peers[peer.GetClusterId()] != nil {
			return errors.New("ambiguous tenant placement entitlement")
		}
		peers[peer.GetClusterId()] = peer
	}
	compiled := proto.CloneOf(payload)
	compiled.SchemaVersion, compiled.MediaPlacement = sharedauthority.PlacementSchemaVersion, proto.CloneOf(snapshot.Own)
	bound := make([]*clusterpb.TenantClusterPeer, 0, len(compiled.GetEffectiveClusterGrants()))
	for _, grant := range compiled.GetEffectiveClusterGrants() {
		peer := peers[grant.GetClusterId()]
		base, _, _, grantErr := tenantGrant(peer, time.Now().UTC())
		if grantErr != nil || !allowed[grant.GetClusterId()] || !proto.Equal(base, grant) || peer.GetMediaConsent() == nil {
			return errors.New("tenant placement grant differs from owner entitlement or lacks consent")
		}
		grant.RegionId, grant.MediaConsent = peer.GetRegionId(), proto.CloneOf(peer.GetMediaConsent())
		bound = append(bound, peer)
	}
	if len(bound) != 0 {
		ownerDigest, digestErr := placement.CommercialEntitlementDigest(payload.GetTenantId(), bound)
		if digestErr != nil {
			return digestErr
		}
		compiledDigest, digestErr := sharedauthority.PlacementCommercialEntitlementDigest(compiled)
		if digestErr != nil || compiledDigest != ownerDigest {
			return errors.New("tenant placement entitlement projection changed owner evidence")
		}
	}
	payload.SchemaVersion, payload.MediaPlacement, payload.EffectiveClusterGrants = compiled.SchemaVersion, compiled.MediaPlacement, compiled.EffectiveClusterGrants
	return nil
}

// The caller holds the version-counter lock so another first publication cannot
// appear between this check and the current-pointer update.
func guardTenantPlacementPublication(ctx context.Context, queries *commodoredb.Queries, payload *mediapb.TenantAuthority) error {
	current, err := queries.LockCurrentTenantMediaAuthority(ctx, payload.GetTenantId())
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	previous, err := decodeTenantPlacementHistory(current.Payload, payload.GetTenantId())
	if err != nil {
		return err
	}
	if previous.GetSchemaVersion() > payload.GetSchemaVersion() || previous.GetMediaPlacement().GetRevision() > payload.GetMediaPlacement().GetRevision() ||
		previous.GetSchemaVersion() == sharedauthority.PlacementSchemaVersion && previous.GetMediaPlacement().GetRevision() == payload.GetMediaPlacement().GetRevision() && !proto.Equal(previous.GetMediaPlacement(), payload.GetMediaPlacement()) {
		return errors.New("tenant authority publication would roll back placement")
	}
	return nil
}
