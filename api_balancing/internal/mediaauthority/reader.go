package mediaauthority

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

type Freshness string

const (
	FreshnessValid       Freshness = "valid"
	FreshnessSoftExpired Freshness = "soft_expired"
	FreshnessHardExpired Freshness = "hard_expired"
)

type TenantSnapshot struct {
	Authority   *mediaauthoritypb.TenantAuthority
	Version     int64
	Ready       bool
	IngestReady bool
	SourceReady bool
	Freshness   Freshness
}

func TenantClusterPeers(tenant *mediaauthoritypb.TenantAuthority) []*clusterpeerpb.TenantClusterPeer {
	if tenant == nil {
		return nil
	}
	peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(tenant.GetEffectiveClusterGrants()))
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant == nil {
			continue
		}
		role := "subscribed"
		if grant.GetClusterId() == tenant.GetPreferredClusterId() {
			role = "preferred"
		} else if grant.GetClusterId() == tenant.GetOfficialClusterId() {
			role = "official"
		}
		peers = append(peers, &clusterpeerpb.TenantClusterPeer{
			ClusterId: grant.GetClusterId(), ClusterType: grant.GetClusterType(), ClusterClass: grant.GetClusterClass(), DeploymentModel: grant.GetDeploymentModel(),
			OwnerTenantId: grant.GetOwnerTenantId(), AccessLevel: grant.GetAccessLevel(), AccessActive: true,
			SubscriptionStatus: grant.GetSubscriptionStatus(), AccessSource: grant.GetAccessSource(), AccessExpiresAt: grant.GetExpiresAt(), ResourceLimits: grant.GetResourceLimits(),
			AllowPrivatePullSources: grant.GetAllowPrivatePullSources(), ControlCellId: grant.GetControlCellId(),
			EligibleServingCellIds: append([]string(nil), grant.GetEligibleServingCellIds()...), Role: role,
		})
	}
	return peers
}

// RoutingClusterPeers overlays local runtime reachability onto signed grants.
// The returned slice is health-filtered routing input. TenantClusterPeers is
// the separate stable authority projection used by promotion comparisons.
func (s *Store) RoutingClusterPeers(tenant *mediaauthoritypb.TenantAuthority, localClusterID string) []*clusterpeerpb.TenantClusterPeer {
	if tenant == nil {
		return nil
	}
	localClusterID = strings.TrimSpace(localClusterID)
	peers := TenantClusterPeers(tenant)
	out := make([]*clusterpeerpb.TenantClusterPeer, 0, len(peers))
	for _, raw := range peers {
		if raw == nil {
			continue
		}
		peer := proto.CloneOf(raw)
		clusterID := strings.TrimSpace(peer.GetClusterId())
		switch {
		case clusterID != "" && clusterID == localClusterID:
			peer.HealthStatus = "healthy"
		case s != nil && s.runtimePeers != nil && s.runtimePeers.IsPeerConnected(clusterID):
			addr := strings.TrimSpace(s.runtimePeers.GetPeerAddr(clusterID))
			if addr == "" {
				continue
			}
			peer.HealthStatus = "healthy"
			peer.FoghornGrpcAddr = addr
		default:
			continue
		}
		if clusterID == strings.TrimSpace(tenant.GetPreferredClusterId()) {
			peer.Role = "preferred"
		} else if clusterID == strings.TrimSpace(tenant.GetOfficialClusterId()) {
			peer.Role = "official"
		} else {
			peer.Role = "subscribed"
		}
		out = append(out, peer)
	}
	return out
}

type MediaObjectSnapshot struct {
	Authority   *mediaauthoritypb.MediaObjectAuthority
	AuthorityID string
	Version     int64
	Ready       bool
	IngestReady bool
	SourceReady bool
	Freshness   Freshness
}

func (s *Store) MediaObjectByPublishingCredential(ctx context.Context, credential string) (MediaObjectSnapshot, error) {
	if credential == "" {
		return MediaObjectSnapshot{}, errors.New("publishing credential is required")
	}
	row, err := foghorndb.New(s.db).GetLocalMediaObjectAuthorityByPublishingCredential(ctx, sharedauthority.PublishingCredentialDigest(credential))
	if err != nil {
		return MediaObjectSnapshot{}, err
	}
	snapshot, err := decodeMediaObjectSnapshot(row.Payload, row.PayloadSha256, row.AuthorityID, row.AuthorityVersion, false, row.RefreshAfter, row.ValidUntil, s.now().UTC())
	snapshot.IngestReady = row.LocalIngestReady
	s.observeFreshness(snapshot.Freshness)
	return snapshot, err
}

func (s *Store) Tenant(ctx context.Context, tenantID string) (TenantSnapshot, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return TenantSnapshot{}, errors.New("tenant ID is required")
	}
	row, err := foghorndb.New(s.db).GetLocalTenantAuthority(ctx, tenantID)
	if err != nil {
		return TenantSnapshot{}, err
	}
	snapshot := TenantSnapshot{
		Version: row.AuthorityVersion, Ready: row.LocalReadReady, IngestReady: row.LocalIngestReady,
		SourceReady: row.LocalSourceReady, Freshness: authorityFreshness(s.now().UTC(), row.RefreshAfter, row.ValidUntil),
	}
	s.observeFreshness(snapshot.Freshness)
	payload := &mediaauthoritypb.TenantAuthority{}
	if err := verifyStoredPayload(row.Payload, row.PayloadSha256); err != nil {
		return snapshot, err
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.Payload, payload); err != nil {
		return snapshot, fmt.Errorf("decode local tenant authority: %w", err)
	}
	if payload.GetTenantId() != tenantID {
		return snapshot, errors.New("local tenant authority identity mismatch")
	}
	snapshot.Authority = payload
	return snapshot, nil
}

func (s *Store) MediaObjectByPlaybackID(ctx context.Context, playbackID string) (MediaObjectSnapshot, error) {
	playbackID = strings.TrimSpace(playbackID)
	if playbackID == "" {
		return MediaObjectSnapshot{}, errors.New("playback ID is required")
	}
	row, err := foghorndb.New(s.db).GetLocalMediaObjectAuthorityByPlaybackID(ctx, playbackID)
	if err != nil {
		return MediaObjectSnapshot{}, err
	}
	snapshot, err := decodeMediaObjectSnapshot(row.Payload, row.PayloadSha256, row.AuthorityID, row.AuthorityVersion, row.LocalReadReady, row.RefreshAfter, row.ValidUntil, s.now().UTC())
	s.observeFreshness(snapshot.Freshness)
	return snapshot, err
}

func (s *Store) MediaObjectByInternalName(ctx context.Context, internalName string) (MediaObjectSnapshot, error) {
	internalName = stripRuntimePrefix(internalName)
	if internalName == "" {
		return MediaObjectSnapshot{}, errors.New("internal name is required")
	}
	row, err := foghorndb.New(s.db).GetLocalMediaObjectAuthorityByInternalName(ctx, internalName)
	if err != nil {
		return MediaObjectSnapshot{}, err
	}
	snapshot, err := decodeMediaObjectSnapshot(row.Payload, row.PayloadSha256, row.AuthorityID, row.AuthorityVersion, row.LocalReadReady, row.RefreshAfter, row.ValidUntil, s.now().UTC())
	s.observeFreshness(snapshot.Freshness)
	return snapshot, err
}

func (s *Store) MediaObjectSourceByInternalName(ctx context.Context, internalName string) (MediaObjectSnapshot, error) {
	internalName = stripRuntimePrefix(internalName)
	if internalName == "" {
		return MediaObjectSnapshot{}, errors.New("internal name is required")
	}
	row, err := foghorndb.New(s.db).GetLocalMediaObjectSourceAuthorityByInternalName(ctx, internalName)
	if err != nil {
		return MediaObjectSnapshot{}, err
	}
	snapshot, err := decodeMediaObjectSnapshot(row.Payload, row.PayloadSha256, row.AuthorityID, row.AuthorityVersion, false, row.RefreshAfter, row.ValidUntil, s.now().UTC())
	snapshot.SourceReady = row.LocalSourceReady
	s.observeFreshness(snapshot.Freshness)
	return snapshot, err
}

func (s *Store) TenantSource(ctx context.Context, tenantID string) (TenantSnapshot, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return TenantSnapshot{}, errors.New("tenant ID is required")
	}
	row, err := foghorndb.New(s.db).GetLocalTenantSourceAuthority(ctx, tenantID)
	if err != nil {
		return TenantSnapshot{}, err
	}
	snapshot := TenantSnapshot{Version: row.AuthorityVersion, SourceReady: row.LocalSourceReady, Freshness: authorityFreshness(s.now().UTC(), row.RefreshAfter, row.ValidUntil)}
	s.observeFreshness(snapshot.Freshness)
	payload := &mediaauthoritypb.TenantAuthority{}
	if err := verifyStoredPayload(row.Payload, row.PayloadSha256); err != nil {
		return snapshot, err
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(row.Payload, payload); err != nil {
		return snapshot, fmt.Errorf("decode local tenant source authority: %w", err)
	}
	if payload.GetTenantId() != tenantID {
		return snapshot, errors.New("local tenant source authority identity mismatch")
	}
	snapshot.Authority = payload
	return snapshot, nil
}

func decodeMediaObjectSnapshot(payloadBytes, payloadDigest []byte, authorityID string, version int64, ready bool, refreshAfter, validUntil, now time.Time) (MediaObjectSnapshot, error) {
	snapshot := MediaObjectSnapshot{
		AuthorityID: authorityID,
		Version:     version,
		Ready:       ready,
		Freshness:   authorityFreshness(now, refreshAfter, validUntil),
	}
	payload := &mediaauthoritypb.MediaObjectAuthority{}
	if err := verifyStoredPayload(payloadBytes, payloadDigest); err != nil {
		return snapshot, err
	}
	if err := (proto.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(payloadBytes, payload); err != nil {
		return snapshot, fmt.Errorf("decode local media-object authority: %w", err)
	}
	snapshot.Authority = payload
	return snapshot, nil
}

func verifyStoredPayload(payload, expectedDigest []byte) error {
	digest := sha256.Sum256(payload)
	if len(expectedDigest) != sha256.Size || subtle.ConstantTimeCompare(digest[:], expectedDigest) != 1 {
		return errors.New("local media authority payload digest mismatch")
	}
	return nil
}

func (s *Store) MarkMediaObjectLocalReadReady(ctx context.Context, authorityID string, version int64) (bool, error) {
	rows, err := foghorndb.New(s.db).MarkMediaObjectAuthorityLocalReadReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalReadReadyParams{
		AuthorityID: strings.TrimSpace(authorityID), AuthorityVersion: version,
	})
	return rows == 1, err
}

func (s *Store) MarkMediaObjectLocalIngestReady(ctx context.Context, authorityID string, version int64) (bool, error) {
	rows, err := foghorndb.New(s.db).MarkMediaObjectAuthorityLocalIngestReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalIngestReadyParams{
		AuthorityID: strings.TrimSpace(authorityID), AuthorityVersion: version,
	})
	return rows == 1, err
}

func (s *Store) MarkMediaObjectLocalSourceReady(ctx context.Context, authorityID string, version int64) (bool, error) {
	rows, err := foghorndb.New(s.db).MarkMediaObjectAuthorityLocalSourceReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalSourceReadyParams{
		AuthorityID: strings.TrimSpace(authorityID), AuthorityVersion: version,
	})
	return rows == 1, err
}

func (s *Store) MarkPlaybackPairLocalReadReady(ctx context.Context, tenantID string, tenantVersion int64, authorityID string, objectVersion int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin local-read promotion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best effort after commit/error
	queries := foghorndb.New(tx)
	tenantRows, err := queries.MarkTenantAuthorityLocalReadReady(ctx, foghorndb.MarkTenantAuthorityLocalReadReadyParams{
		TenantID: strings.TrimSpace(tenantID), AuthorityVersion: tenantVersion,
	})
	if err != nil || tenantRows != 1 {
		return false, fmt.Errorf("promote tenant local read: rows=%d: %w", tenantRows, err)
	}
	objectRows, err := queries.MarkMediaObjectAuthorityLocalReadReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalReadReadyParams{
		AuthorityID: strings.TrimSpace(authorityID), AuthorityVersion: objectVersion,
	})
	if err != nil || objectRows != 1 {
		return false, fmt.Errorf("promote media-object local read: rows=%d: %w", objectRows, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit local-read promotion: %w", err)
	}
	return true, nil
}

func (s *Store) MarkIngestPairLocalReady(ctx context.Context, tenantID string, tenantVersion int64, authorityID string, objectVersion int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin local-ingest promotion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best effort after commit/error
	queries := foghorndb.New(tx)
	tenantRows, err := queries.MarkTenantAuthorityLocalIngestReady(ctx, foghorndb.MarkTenantAuthorityLocalIngestReadyParams{
		TenantID: strings.TrimSpace(tenantID), AuthorityVersion: tenantVersion,
	})
	if err != nil || tenantRows != 1 {
		return false, fmt.Errorf("promote tenant local ingest: rows=%d: %w", tenantRows, err)
	}
	objectRows, err := queries.MarkMediaObjectAuthorityLocalIngestReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalIngestReadyParams{
		AuthorityID: strings.TrimSpace(authorityID), AuthorityVersion: objectVersion,
	})
	if err != nil || objectRows != 1 {
		return false, fmt.Errorf("promote media-object local ingest: rows=%d: %w", objectRows, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit local-ingest promotion: %w", err)
	}
	return true, nil
}

func (s *Store) MarkSourcePairLocalReady(ctx context.Context, tenantID string, tenantVersion int64, authorityID string, objectVersion int64) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin local-source promotion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // best effort after commit/error
	queries := foghorndb.New(tx)
	tenantRows, err := queries.MarkTenantAuthorityLocalSourceReady(ctx, foghorndb.MarkTenantAuthorityLocalSourceReadyParams{
		TenantID: strings.TrimSpace(tenantID), AuthorityVersion: tenantVersion,
	})
	if err != nil || tenantRows != 1 {
		return false, fmt.Errorf("promote tenant local source: rows=%d: %w", tenantRows, err)
	}
	objectRows, err := queries.MarkMediaObjectAuthorityLocalSourceReady(ctx, foghorndb.MarkMediaObjectAuthorityLocalSourceReadyParams{
		AuthorityID: strings.TrimSpace(authorityID), AuthorityVersion: objectVersion,
	})
	if err != nil || objectRows != 1 {
		return false, fmt.Errorf("promote media-object local source: rows=%d: %w", objectRows, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit local-source promotion: %w", err)
	}
	return true, nil
}

func authorityFreshness(now, refreshAfter, validUntil time.Time) Freshness {
	if !now.Before(validUntil.UTC()) {
		return FreshnessHardExpired
	}
	if !now.Before(refreshAfter.UTC()) {
		return FreshnessSoftExpired
	}
	return FreshnessValid
}

func stripRuntimePrefix(value string) string {
	value = strings.TrimSpace(value)
	for _, prefix := range []string{"live+", "pull+", "vod+", "dvr+"} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}
