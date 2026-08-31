package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
)

type ManagedStreamSet struct {
	Rows     []*commodorepb.ManagedStreamRow
	Contexts map[string]*commodorepb.ResolveStreamContextResponse
	Marked   bool
	Complete bool
}

type ManagedStreamPromotion string

const (
	ManagedStreamPromotionNone     ManagedStreamPromotion = ""
	ManagedStreamPromotionMatched  ManagedStreamPromotion = "matched"
	ManagedStreamPromotionMismatch ManagedStreamPromotion = "mismatch"
)

// ManagedStreams returns a complete desired-state view only when every
// locally known mist_native object and its tenant projection carry the exact
// source-readiness marker. A partial set is never used for retract decisions.
func (s *Store) ManagedStreams(ctx context.Context, clusterID string) (ManagedStreamSet, error) {
	result := ManagedStreamSet{Contexts: make(map[string]*commodorepb.ResolveStreamContextResponse)}
	rows, err := foghorndb.New(s.db).ListLocalManagedStreamAuthorities(ctx)
	if err != nil {
		return result, fmt.Errorf("list local managed-stream authorities: %w", err)
	}
	if len(rows) == 0 {
		return result, nil
	}
	result.Complete = true
	for _, row := range rows {
		result.Marked = result.Marked || row.LocalSourceReady
		object, err := decodeMediaObjectSnapshot(row.Payload, row.PayloadSha256, row.AuthorityID, row.AuthorityVersion, false, row.RefreshAfter, row.ValidUntil, s.now().UTC())
		if err != nil {
			return result, err
		}
		object.SourceReady = row.LocalSourceReady
		if !object.SourceReady {
			result.Complete = false
			continue
		}
		if object.Freshness == FreshnessHardExpired {
			return result, fmt.Errorf("managed-stream authority %q hard-expired", row.AuthorityID)
		}
		authority := object.Authority
		if authority == nil || authority.GetLiveStream() == nil {
			return result, fmt.Errorf("managed-stream authority %q has no live-stream payload", row.AuthorityID)
		}
		tenant, err := s.TenantSource(ctx, authority.GetTenantId())
		if err != nil {
			return result, fmt.Errorf("load managed-stream tenant %q: %w", authority.GetTenantId(), err)
		}
		if !tenant.SourceReady {
			result.Complete = false
			continue
		}
		if tenant.Freshness == FreshnessHardExpired {
			return result, fmt.Errorf("managed-stream tenant authority %q hard-expired", authority.GetTenantId())
		}
		if authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
			tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
			tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
			continue
		}
		if !tenantAllowsCluster(tenant.Authority, clusterID) {
			continue
		}
		secret, err := s.OpenLiveStreamSecret(object)
		if err != nil {
			return result, fmt.Errorf("open managed-stream authority %q: %w", row.AuthorityID, err)
		}
		if strings.TrimSpace(secret.GetNativeSourceSpec()) == "" || !slices.Contains(secret.GetNativeAllowedClusterIds(), clusterID) {
			continue
		}
		live := authority.GetLiveStream()
		managed := &commodorepb.ManagedStreamRow{
			StreamId: live.GetStreamId(), PlaybackId: authority.GetPlaybackId(), InternalName: authority.GetInternalName(),
			TenantId: authority.GetTenantId(), IngestMode: live.GetIngestMode(), SourceSpec: secret.GetNativeSourceSpec(),
			SourceKind: secret.GetNativeSourceKind(), AlwaysOn: secret.GetNativeAlwaysOn(), PlacementCount: secret.GetNativePlacementCount(),
			AllowedClusterIds: append([]string(nil), secret.GetNativeAllowedClusterIds()...),
		}
		result.Rows = append(result.Rows, managed)
		origin, official := authority.GetOriginClusterId(), tenant.Authority.GetOfficialClusterId()
		result.Contexts[live.GetStreamId()] = &commodorepb.ResolveStreamContextResponse{
			Admitted: true, StreamId: live.GetStreamId(), PlaybackId: authority.GetPlaybackId(), InternalName: authority.GetInternalName(),
			IngestMode: live.GetIngestMode(), TenantId: authority.GetTenantId(), UserId: authority.GetUserId(),
			IsRecordingEnabled: live.GetRecordingEnabled(), RequiresAuth: authority.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC,
			BillingModel: localBillingModel(tenant.Authority.GetBillingModel()), OriginClusterId: &origin, OfficialClusterId: &official,
			ClusterPeers: s.RoutingClusterPeers(tenant.Authority, clusterID), AuthorityClusterPeers: TenantClusterPeers(tenant.Authority), ProcessesJson: live.GetProcessesJson(), DvrProcessesJson: live.GetDvrProcessesJson(),
			DvrPolicy: tenant.Authority.GetDvrPolicy(), Allowances: tenant.Authority.GetAllowances(), TenantResourceLimits: clusterResourceLimits(tenant.Authority, clusterID),
		}
	}
	return result, nil
}

func (s *Store) PromoteManagedStreamIfMatching(ctx context.Context, clusterID string, connected *commodorepb.ManagedStreamRow, streamCtx *commodorepb.ResolveStreamContextResponse) (ManagedStreamPromotion, error) {
	if s == nil || connected == nil || streamCtx == nil || !streamCtx.GetAdmitted() {
		return ManagedStreamPromotionNone, nil
	}
	object, err := s.MediaObjectSourceByInternalName(ctx, connected.GetInternalName())
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedStreamPromotionNone, nil
	}
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	tenant, err := s.TenantSource(ctx, object.Authority.GetTenantId())
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	if object.SourceReady {
		return ManagedStreamPromotionNone, nil
	}
	if object.Freshness == FreshnessHardExpired || tenant.Freshness == FreshnessHardExpired {
		return ManagedStreamPromotionMismatch, nil
	}
	secret, err := s.OpenLiveStreamSecret(object)
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	wantAllowed := append([]string(nil), secret.GetNativeAllowedClusterIds()...)
	gotAllowed := append([]string(nil), connected.GetAllowedClusterIds()...)
	slices.Sort(wantAllowed)
	slices.Sort(gotAllowed)
	authority, live := object.Authority, object.Authority.GetLiveStream()
	localLimits := clusterResourceLimits(tenant.Authority, clusterID)
	if authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW ||
		live.GetStreamId() != connected.GetStreamId() || authority.GetPlaybackId() != connected.GetPlaybackId() ||
		authority.GetInternalName() != connected.GetInternalName() || authority.GetTenantId() != connected.GetTenantId() ||
		live.GetIngestMode() != connected.GetIngestMode() || secret.GetNativeSourceSpec() != connected.GetSourceSpec() ||
		secret.GetNativeSourceKind() != connected.GetSourceKind() || secret.GetNativeAlwaysOn() != connected.GetAlwaysOn() ||
		secret.GetNativePlacementCount() != connected.GetPlacementCount() || !slices.Equal(wantAllowed, gotAllowed) ||
		streamCtx.GetStreamId() != live.GetStreamId() || streamCtx.GetTenantId() != authority.GetTenantId() ||
		streamCtx.GetUserId() != authority.GetUserId() || streamCtx.GetInternalName() != authority.GetInternalName() ||
		streamCtx.GetPlaybackId() != authority.GetPlaybackId() || streamCtx.GetIngestMode() != live.GetIngestMode() ||
		streamCtx.GetIsRecordingEnabled() != live.GetRecordingEnabled() || streamCtx.GetProcessesJson() != live.GetProcessesJson() ||
		streamCtx.GetDvrProcessesJson() != live.GetDvrProcessesJson() || streamCtx.GetBillingModel() != localBillingModel(tenant.Authority.GetBillingModel()) ||
		streamCtx.GetOfficialClusterId() != tenant.Authority.GetOfficialClusterId() || !proto.Equal(streamCtx.GetDvrPolicy(), tenant.Authority.GetDvrPolicy()) ||
		!sharedauthority.SameResourceLimits(streamCtx.GetTenantResourceLimits(), localLimits) ||
		!sharedauthority.SameTenantClusterGrants(tenant.Authority, streamCtx.GetAuthorityClusterPeers()) ||
		!sharedauthority.SamePreferredCluster(tenant.Authority, streamCtx.GetAuthorityClusterPeers()) ||
		!sharedauthority.SameAllowances(streamCtx.GetAllowances(), tenant.Authority.GetAllowances()) {
		return ManagedStreamPromotionMismatch, nil
	}
	var marked bool
	if tenant.SourceReady {
		marked, err = s.MarkMediaObjectLocalSourceReady(ctx, object.AuthorityID, object.Version)
	} else {
		marked, err = s.MarkSourcePairLocalReady(ctx, authority.GetTenantId(), tenant.Version, object.AuthorityID, object.Version)
	}
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	if marked {
		return ManagedStreamPromotionMatched, nil
	}
	return ManagedStreamPromotionNone, nil
}

func tenantAllowsCluster(tenant *mediaauthoritypb.TenantAuthority, clusterID string) bool {
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant.GetClusterId() == strings.TrimSpace(clusterID) {
			return true
		}
	}
	return false
}

func clusterResourceLimits(tenant *mediaauthoritypb.TenantAuthority, clusterID string) *tenantlimitspb.TenantResourceLimits {
	return sharedauthority.EffectiveResourceLimits(tenant, clusterID)
}

func localBillingModel(value mediaauthoritypb.TenantBillingModel) string {
	if value == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID {
		return "prepaid"
	}
	return "postpaid"
}
