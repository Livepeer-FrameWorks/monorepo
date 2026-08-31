package triggers

import (
	"context"
	"errors"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func (p *Processor) localPullSource(ctx context.Context, internalName string) (*commodorepb.ResolvePullSourceByInternalNameResponse, localauthority.SourceSnapshot, bool, bool, error) {
	if p == nil || p.mediaAuthorityStore == nil {
		return nil, localauthority.SourceSnapshot{}, false, false, nil
	}
	resolution, err := control.ResolveLocalPullSource(ctx, p.mediaAuthorityStore, internalName)
	return resolution.Response, resolution.Snapshot, resolution.Found, resolution.Marked, err
}

func (p *Processor) promoteLocalPullSourceIfMatching(ctx context.Context, connected *commodorepb.ResolvePullSourceByInternalNameResponse, snapshot localauthority.SourceSnapshot) {
	if p == nil || p.mediaAuthorityStore == nil {
		return
	}
	var streamCtx *commodorepb.ResolveStreamContextResponse
	if p.commodoreClient != nil && connected != nil {
		var contextErr error
		// Promotion compares durable authority. Passing an operational cluster
		// arms connected-mode health/admission gates and makes local cutover fail
		// precisely when that cluster is degraded.
		streamCtx, contextErr = p.commodoreClient.ResolveStreamContext(ctx, connected.GetStreamId(), "", "", "")
		if contextErr != nil {
			p.logger.WithError(contextErr).WithField("stream_id", connected.GetStreamId()).Debug("Connected stream context unavailable for local pull-source promotion")
		}
	}
	promotion, err := control.PromoteLocalPullSourceIfMatching(ctx, p.mediaAuthorityStore, connected, streamCtx, snapshot)
	if promotion == control.PullSourcePromotionMismatch {
		p.observeMediaAuthorityShadow("pull_source_mismatch")
		return
	}
	if err != nil {
		p.logger.WithError(err).WithField("authority_id", snapshot.Object.AuthorityID).Warn("Failed to promote matching local pull-source authority")
		return
	}
	if promotion == control.PullSourcePromotionMatched {
		p.observeMediaAuthorityShadow("pull_source_pair_promoted")
	}
}

func localTenantAllowsSourceCluster(tenant *mediaauthoritypb.TenantAuthority, clusterID string, private bool) bool {
	return control.LocalTenantAllowsSourceCluster(tenant, clusterID, private)
}

func (p *Processor) localArtifactSource(ctx context.Context, internalName string) (*commodorepb.ResolveArtifactInternalNameResponse, localauthority.SourceSnapshot, bool, bool, error) {
	if p == nil || p.mediaAuthorityStore == nil {
		return nil, localauthority.SourceSnapshot{}, false, false, nil
	}
	snapshot, found, err := p.mediaAuthorityStore.ArtifactSource(ctx, internalName)
	if !found {
		return nil, snapshot, false, false, err
	}
	marked := snapshot.Object.SourceReady
	if snapshot.Object.SourceReady && !snapshot.Tenant.SourceReady {
		return nil, snapshot, true, true, errors.New("local artifact source object is ready before its tenant")
	}
	if !marked {
		return nil, snapshot, true, false, err
	}
	if err != nil {
		return nil, snapshot, true, true, err
	}
	if snapshot.Object.Freshness == localauthority.FreshnessHardExpired || snapshot.Tenant.Freshness == localauthority.FreshnessHardExpired {
		return nil, snapshot, true, true, errLocalAuthorityExpired
	}
	object, tenant := snapshot.Object.Authority, snapshot.Tenant.Authority
	if object.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return &commodorepb.ResolveArtifactInternalNameResponse{Found: false}, snapshot, true, true, nil
	}
	artifact := object.GetArtifact()
	return &commodorepb.ResolveArtifactInternalNameResponse{
		Found: true, ArtifactHash: artifact.GetArtifactHash(), InternalName: object.GetInternalName(),
		TenantId: object.GetTenantId(), UserId: object.GetUserId(), StreamId: artifact.GetParentStreamId(),
		ContentType: localArtifactKind(artifact.GetArtifactKind()), OriginClusterId: object.GetOriginClusterId(),
		ClusterPeers: p.mediaAuthorityStore.RoutingClusterPeers(tenant, p.clusterID), AuthorityClusterPeers: localClusterPeers(tenant), RequiresAuth: object.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC,
		ParentStreamInternalName: artifact.GetParentStreamInternalName(),
	}, snapshot, true, true, nil
}

func (p *Processor) promoteLocalArtifactSourceIfMatching(ctx context.Context, connected *commodorepb.ResolveArtifactInternalNameResponse, snapshot localauthority.SourceSnapshot) {
	if p == nil || p.mediaAuthorityStore == nil || connected == nil || !connected.GetFound() ||
		snapshot.Object.Authority == nil || snapshot.Tenant.Authority == nil || snapshot.Object.SourceReady {
		return
	}
	readObject, objectErr := p.mediaAuthorityStore.MediaObjectByInternalName(ctx, snapshot.Object.Authority.GetInternalName())
	readTenant, tenantErr := p.mediaAuthorityStore.Tenant(ctx, snapshot.Object.Authority.GetTenantId())
	if objectErr != nil || tenantErr != nil || !readObject.Ready || !readTenant.Ready ||
		readObject.Freshness == localauthority.FreshnessHardExpired || readTenant.Freshness == localauthority.FreshnessHardExpired ||
		readObject.Version != snapshot.Object.Version || readTenant.Version != snapshot.Tenant.Version ||
		!proto.Equal(readObject.Authority, snapshot.Object.Authority) || !proto.Equal(readTenant.Authority, snapshot.Tenant.Authority) ||
		!sameLocalArtifactSource(snapshot, connected) {
		p.observeMediaAuthorityShadow("artifact_source_mismatch")
		return
	}
	var marked bool
	var err error
	if snapshot.Tenant.SourceReady {
		marked, err = p.mediaAuthorityStore.MarkMediaObjectLocalSourceReady(ctx, snapshot.Object.AuthorityID, snapshot.Object.Version)
	} else {
		marked, err = p.mediaAuthorityStore.MarkSourcePairLocalReady(ctx, snapshot.Object.Authority.GetTenantId(), snapshot.Tenant.Version, snapshot.Object.AuthorityID, snapshot.Object.Version)
	}
	if err != nil {
		p.logger.WithError(err).WithField("authority_id", snapshot.Object.AuthorityID).Warn("Failed to promote matching local artifact-source authority")
		return
	}
	if marked {
		p.observeMediaAuthorityShadow("artifact_source_pair_promoted")
	}
}

func sameLocalArtifactSource(snapshot localauthority.SourceSnapshot, connected *commodorepb.ResolveArtifactInternalNameResponse) bool {
	object, tenant := snapshot.Object.Authority, snapshot.Tenant.Authority
	if object == nil || object.GetArtifact() == nil || tenant == nil || connected == nil || !connected.GetFound() ||
		object.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return false
	}
	artifact := object.GetArtifact()
	if artifact.GetArtifactHash() != connected.GetArtifactHash() || object.GetInternalName() != connected.GetInternalName() ||
		object.GetTenantId() != connected.GetTenantId() || object.GetUserId() != connected.GetUserId() ||
		artifact.GetParentStreamId() != connected.GetStreamId() || localArtifactKind(artifact.GetArtifactKind()) != connected.GetContentType() ||
		object.GetOriginClusterId() != connected.GetOriginClusterId() ||
		artifact.GetParentStreamInternalName() != connected.GetParentStreamInternalName() ||
		(object.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC) != connected.GetRequiresAuth() {
		return false
	}
	return sharedauthority.SameTenantClusterGrants(tenant, connected.GetAuthorityClusterPeers()) &&
		sharedauthority.SamePreferredCluster(tenant, connected.GetAuthorityClusterPeers())
}
