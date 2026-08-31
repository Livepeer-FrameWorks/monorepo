package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

type localViewerAuthority struct {
	object localauthority.MediaObjectSnapshot
	tenant localauthority.TenantSnapshot
}

func (s *FoghornGRPCServer) resolveLocalViewerContent(ctx context.Context, contentID string) (*control.ContentResolution, localViewerAuthority, bool, error) {
	if s.mediaAuthorityStore == nil {
		return nil, localViewerAuthority{}, false, nil
	}
	object, err := s.mediaAuthorityStore.MediaObjectByPlaybackID(ctx, contentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, localViewerAuthority{}, false, nil
	}
	if err != nil {
		return nil, localViewerAuthority{}, false, fmt.Errorf("read local media-object authority: %w", err)
	}
	local := localViewerAuthority{object: object}
	if !object.Ready {
		return nil, local, false, nil
	}
	if object.Freshness == localauthority.FreshnessHardExpired {
		return nil, local, true, errors.New("local media-object authority hard-expired")
	}
	if object.Authority == nil || object.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		return nil, local, true, sql.ErrNoRows
	}
	tenant, err := s.mediaAuthorityStore.Tenant(ctx, object.Authority.GetTenantId())
	if err != nil {
		return nil, local, false, fmt.Errorf("read local tenant authority: %w", err)
	}
	local.tenant = tenant
	if !tenant.Ready {
		return nil, local, false, nil
	}
	if tenant.Freshness == localauthority.FreshnessHardExpired {
		return nil, local, true, errors.New("local tenant authority hard-expired")
	}
	if tenant.Authority == nil || tenant.Authority.GetTenantId() != object.Authority.GetTenantId() {
		return nil, local, false, errors.New("local media and tenant authority identity mismatch")
	}
	resolution := localContentResolution(contentID, object.Authority, tenant.Authority)
	resolution.ClusterPeers = s.mediaAuthorityStore.RoutingClusterPeers(tenant.Authority, s.clusterID)
	if artifact := object.Authority.GetArtifact(); artifact != nil {
		if host, _ := state.DefaultManager().FindNodeByArtifactHash(artifact.GetArtifactHash()); host != "" {
			resolution.FixedNode = host
			if s.lb != nil {
				resolution.FixedNodeID = s.lb.GetNodeIDByHost(host)
			}
		}
	}
	return resolution, local, true, nil
}

func localContentResolution(contentID string, object *mediaauthoritypb.MediaObjectAuthority, tenant *mediaauthoritypb.TenantAuthority) *control.ContentResolution {
	resolution := &control.ContentResolution{
		ContentId: contentID, TenantId: object.GetTenantId(), UserId: object.GetUserId(), InternalName: object.GetInternalName(),
		OriginClusterID: object.GetOriginClusterId(), LocalAuthority: true, ActiveIngestClusterID: object.GetOriginClusterId(),
		ClusterPeers:                localauthority.TenantClusterPeers(tenant),
		AuthorityClusterPeers:       localauthority.TenantClusterPeers(tenant),
		OfficialClusterID:           tenant.GetOfficialClusterId(),
		AllowPlatformSharedPlayback: tenant.GetAllowPlatformSharedPlayback(),
		RequiresAuth:                object.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC,
	}
	if live := object.GetLiveStream(); live != nil {
		resolution.ContentType = "live"
		resolution.StreamId = live.GetStreamId()
		resolution.IngestMode = live.GetIngestMode()
		resolution.ActiveIngestClusterID = object.GetOriginClusterId()
		return resolution
	}
	artifact := object.GetArtifact()
	resolution.ArtifactHash = artifact.GetArtifactHash()
	resolution.StreamId = artifact.GetParentStreamId()
	resolution.ParentStreamInternalName = artifact.GetParentStreamInternalName()
	switch artifact.GetArtifactKind() {
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_DVR:
		resolution.ContentType = "dvr"
		resolution.InternalName = "dvr+" + object.GetInternalName()
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP:
		resolution.ContentType = "clip"
		resolution.InternalName = "vod+" + object.GetInternalName()
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CHAPTER:
		resolution.ContentType = "chapter"
		resolution.InternalName = "vod+" + object.GetInternalName()
	default:
		resolution.ContentType = "vod"
		resolution.InternalName = "vod+" + object.GetInternalName()
	}
	return resolution
}

func localTenantDenial(tenant *mediaauthoritypb.TenantAuthority) (paymentRequired, denied bool) {
	if tenant == nil || tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		return false, true
	}
	switch tenant.GetBillingDecision() {
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW:
		return false, false
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED,
		mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED:
		return true, true
	default:
		return false, true
	}
}
