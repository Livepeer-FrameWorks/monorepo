package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

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
//
// Federation discovery, admission and ingest resolution decide on this pair
// alone, so it is where a cell that holds nothing for the object, or holds it
// past its validity, asks for it: every cell a request reaches repairs its own
// authority. The fetched versions go through Apply and its version and policy
// fences like any delivery; when nothing can be fetched the read stands.
func (s *Store) Placement(ctx context.Context, tenantID, authorityID, internalName string) (PlacementPair, error) {
	if tenantID == "" || authorityID == "" || internalName == "" ||
		strings.TrimSpace(tenantID) != tenantID || strings.TrimSpace(authorityID) != authorityID || strings.TrimSpace(internalName) != internalName {
		return PlacementPair{}, errors.New("placement authority identity is required")
	}
	read := func(ctx context.Context) (PlacementPair, error) {
		row, err := foghorndb.New(s.db).GetLocalPlacementAuthorityPair(ctx, foghorndb.GetLocalPlacementAuthorityPairParams{
			TenantID: tenantID, AuthorityID: authorityID, InternalName: internalName,
		})
		if err != nil {
			return PlacementPair{}, err
		}
		return s.decodePlacementPair(row, tenantID, authorityID, internalName)
	}
	return s.readPlacementFetchingOnMiss(ctx, AuthorityLookup{AuthorityID: authorityID}, read)
}

// PlacementForInternalName resolves final-admission identity within the supplied
// tenant using one joined snapshot, without an unscoped object lookup first. It
// asks for a missing or expired pair like Placement.
func (s *Store) PlacementForInternalName(ctx context.Context, tenantID, internalName string) (PlacementPair, error) {
	if tenantID == "" || internalName == "" || strings.TrimSpace(tenantID) != tenantID || strings.TrimSpace(internalName) != internalName {
		return PlacementPair{}, errors.New("placement tenant and internal name are required")
	}
	read := func(ctx context.Context) (PlacementPair, error) {
		row, err := foghorndb.New(s.db).GetLocalPlacementAuthorityPairByInternalName(ctx, foghorndb.GetLocalPlacementAuthorityPairByInternalNameParams{TenantID: tenantID, InternalName: internalName})
		if err != nil {
			return PlacementPair{}, err
		}
		return s.decodePlacementPair(foghorndb.GetLocalPlacementAuthorityPairRow(row), tenantID, row.ObjectAuthorityID, internalName)
	}
	return s.readPlacementFetchingOnMiss(ctx, AuthorityLookup{InternalName: internalName}, read)
}

// placementLocalReadTimeout bounds one read of the local pair.
const placementLocalReadTimeout = time.Second

// PlacementReadTimeout is what a caller allows a placement read: a local read,
// a fetch when the pair is missing or expired, and the read again. Each step
// keeps its own bound, so a slow database never eats the fetch's time and a
// fetch never extends a local read; the caller's own deadline still ends all of
// them.
const PlacementReadTimeout = 2*placementLocalReadTimeout + authorityFetchTimeout

// readPlacementFetchingOnMiss reads the pair, and when either half is missing
// or past its validity asks for the object once and reads again. A tombstoned
// object is an answer and is never asked around.
func (s *Store) readPlacementFetchingOnMiss(ctx context.Context, lookup AuthorityLookup, read func(context.Context) (PlacementPair, error)) (PlacementPair, error) {
	readLocal := func() (PlacementPair, error) {
		readCtx, stop := context.WithTimeout(ctx, placementLocalReadTimeout)
		defer stop()
		return read(readCtx)
	}
	pair, err := readLocal()
	if !placementPairNeedsFetch(pair, err, s.now().UTC()) {
		return pair, err
	}
	if applied, _ := s.Fetch(ctx, lookup); !applied { //nolint:errcheck // a fetch that cannot be made leaves the read as it was
		return pair, err
	}
	return readLocal()
}

func placementPairNeedsFetch(pair PlacementPair, err error, now time.Time) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	if err != nil {
		return false
	}
	if pair.Object.Authority.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE ||
		pair.Tenant.Authority.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		return false
	}
	// Freshness also carries the restore fence, which withholds a pair the cell
	// may hold stale.
	return !now.Before(pair.Object.ValidUntil) || !now.Before(pair.Tenant.ValidUntil) ||
		pair.Object.Freshness == FreshnessHardExpired || pair.Tenant.Freshness == FreshnessHardExpired
}

func (s *Store) decodePlacementPair(row foghorndb.GetLocalPlacementAuthorityPairRow, tenantID, authorityID, internalName string) (PlacementPair, error) {
	now := s.now().UTC()
	object, err := decodeMediaObjectSnapshot(row.ObjectPayload, row.ObjectPayloadSha256, row.ObjectAuthorityID, row.ObjectAuthorityVersion,
		row.ObjectReadReady, row.ObjectRefreshAfter, row.ObjectValidUntil, now)
	if err != nil {
		return PlacementPair{}, err
	}
	object.ParentVersion = row.TenantAuthorityVersion
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
	result.Tenant.Freshness = s.fencedFreshness(result.Tenant.Freshness, "tenant", tenant.GetTenantId(), false)
	result.Object.Freshness = s.fencedFreshness(result.Object.Freshness, "media_object", object.AuthorityID, row.ObjectWithheldByTenantRevival)
	s.observeFreshness(result.Tenant.Freshness, AuthorityLookup{TenantID: tenant.GetTenantId()})
	s.observeFreshness(result.Object.Freshness, AuthorityLookup{AuthorityID: object.AuthorityID})
	return result, nil
}
