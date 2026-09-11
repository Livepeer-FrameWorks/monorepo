package mediaauthority

import (
	"context"
	"errors"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

// PlacementPair contains one database snapshot of the signed tenant and object
// projections. It carries no decrypted publishing or source credentials.
type PlacementPair struct {
	Tenant TenantSnapshot
	Object MediaObjectSnapshot
}

// Placement reads by tenant, authority identity and internal name together.
// One joined statement prevents a refresh between separate tenant/object reads;
// the evaluator still requires the object's exact parent policy revision.
func (s *Store) Placement(ctx context.Context, tenantID, authorityID, internalName string) (PlacementPair, error) {
	if tenantID == "" || authorityID == "" || internalName == "" ||
		strings.TrimSpace(tenantID) != tenantID || strings.TrimSpace(authorityID) != authorityID || strings.TrimSpace(internalName) != internalName {
		return PlacementPair{}, errors.New("placement authority identity is required")
	}
	row, err := foghorndb.New(s.db).GetLocalPlacementAuthorityPair(ctx, foghorndb.GetLocalPlacementAuthorityPairParams{
		TenantID: tenantID, AuthorityID: authorityID, InternalName: internalName,
	})
	if err != nil {
		return PlacementPair{}, err
	}
	return s.decodePlacementPair(row, tenantID, authorityID, internalName)
}

// PlacementForInternalName resolves final-admission identity within the supplied
// tenant using one joined snapshot, without an unscoped object lookup first.
func (s *Store) PlacementForInternalName(ctx context.Context, tenantID, internalName string) (PlacementPair, error) {
	if tenantID == "" || internalName == "" || strings.TrimSpace(tenantID) != tenantID || strings.TrimSpace(internalName) != internalName {
		return PlacementPair{}, errors.New("placement tenant and internal name are required")
	}
	row, err := foghorndb.New(s.db).GetLocalPlacementAuthorityPairByInternalName(ctx, foghorndb.GetLocalPlacementAuthorityPairByInternalNameParams{TenantID: tenantID, InternalName: internalName})
	if err != nil {
		return PlacementPair{}, err
	}
	return s.decodePlacementPair(foghorndb.GetLocalPlacementAuthorityPairRow(row), tenantID, row.ObjectAuthorityID, internalName)
}

func (s *Store) decodePlacementPair(row foghorndb.GetLocalPlacementAuthorityPairRow, tenantID, authorityID, internalName string) (PlacementPair, error) {
	now := s.now().UTC()
	object, err := decodeMediaObjectSnapshot(row.ObjectPayload, row.ObjectPayloadSha256, row.ObjectAuthorityID, row.ObjectAuthorityVersion,
		row.ObjectReadReady, row.ObjectRefreshAfter, row.ObjectValidUntil, now)
	if err != nil {
		return PlacementPair{}, err
	}
	object.IngestReady, object.SourceReady = row.ObjectIngestReady, row.ObjectSourceReady
	if object.Authority.GetTenantId() != tenantID || object.Authority.GetInternalName() != internalName || object.AuthorityID != authorityID {
		return PlacementPair{}, errors.New("placement object authority identity mismatch")
	}
	switch object.Authority.GetObjectKind() {
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		if object.Authority.GetLiveStream().GetStreamId() == "" || authorityID != sharedauthority.LiveStreamAuthorityID(object.Authority.GetLiveStream().GetStreamId()) {
			return PlacementPair{}, errors.New("placement live authority identity mismatch")
		}
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		if object.Authority.GetArtifact().GetArtifactId() == "" || authorityID != sharedauthority.ArtifactAuthorityID(object.Authority.GetArtifact().GetArtifactId()) {
			return PlacementPair{}, errors.New("placement artifact authority identity mismatch")
		}
	default:
		return PlacementPair{}, errors.New("placement object kind is unsupported")
	}
	if err := verifyStoredPayload(row.TenantPayload, row.TenantPayloadSha256); err != nil {
		return PlacementPair{}, err
	}
	tenant := &mediaauthoritypb.TenantAuthority{}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.TenantPayload, tenant); err != nil {
		return PlacementPair{}, errors.New("decode placement tenant authority")
	}
	if tenant.GetTenantId() != tenantID {
		return PlacementPair{}, errors.New("placement tenant authority identity mismatch")
	}
	result := PlacementPair{Object: object, Tenant: TenantSnapshot{
		Authority: tenant, Version: row.TenantAuthorityVersion,
		Ready: row.TenantReadReady, IngestReady: row.TenantIngestReady, SourceReady: row.TenantSourceReady,
		RefreshAfter: row.TenantRefreshAfter, ValidUntil: row.TenantValidUntil,
		Freshness: authorityFreshness(now, row.TenantRefreshAfter, row.TenantValidUntil),
	}}
	s.observeFreshness(result.Tenant.Freshness)
	s.observeFreshness(result.Object.Freshness)
	return result, nil
}
