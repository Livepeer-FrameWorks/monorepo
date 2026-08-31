package triggers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

var (
	errLocalAuthorityExpired = errors.New("local media authority hard-expired")
	errLocalAuthorityDenied  = errors.New("local media authority denies access")
)

// IsLocalAuthorityDenied distinguishes an authoritative local policy denial
// from an unavailable/corrupt local decision. Callers may still use the
// accompanying resolution for identity and audit fields, but must not route it.
func IsLocalAuthorityDenied(err error) bool {
	return errors.Is(err, errLocalAuthorityDenied)
}

// IsLocalAuthorityExpired identifies a projection that was once promoted but
// has crossed its signed hard-validity bound. This is terminal for the local
// decision and must not be presented as a transient database/read failure.
func IsLocalAuthorityExpired(err error) bool {
	return errors.Is(err, errLocalAuthorityExpired)
}

type localPlaybackAuthority struct {
	target *control.StreamTarget
	info   streamContext
	object localauthority.MediaObjectSnapshot
	tenant localauthority.TenantSnapshot
}

func (p *Processor) ResolveLocalContent(ctx context.Context, input string) (*control.ContentResolution, bool, error) {
	local, found, err := p.resolveReadyLocalPlayback(ctx, input, true)
	if !found && err == nil {
		local, found, err = p.resolveReadyLocalPlayback(ctx, input, false)
	}
	if errors.Is(err, errLocalAuthorityDenied) && local.object.Authority != nil {
		if local.object.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || local.tenant.Authority == nil {
			return nil, true, sql.ErrNoRows
		}
		local.target = localStreamTarget(local.object.Authority, local.tenant.Authority)
		local.target.ClusterPeers = p.mediaAuthorityStore.RoutingClusterPeers(local.tenant.Authority, p.clusterID)
	}
	if (err != nil && !errors.Is(err, errLocalAuthorityDenied)) || !found || local.target == nil {
		return nil, found && err != nil, err
	}
	object := local.object.Authority
	resolution := &control.ContentResolution{
		ContentType: local.target.ContentType, ContentId: object.GetPlaybackId(), TenantId: object.GetTenantId(),
		UserId: object.GetUserId(), StreamId: local.target.StreamID, InternalName: local.target.InternalName,
		OriginClusterID: object.GetOriginClusterId(), ClusterPeers: local.target.ClusterPeers,
		AuthorityClusterPeers:       local.info.AuthorityClusterPeers,
		OfficialClusterID:           local.tenant.Authority.GetOfficialClusterId(),
		AllowPlatformSharedPlayback: local.tenant.Authority.GetAllowPlatformSharedPlayback(),
		RequiresAuth:                local.target.RequiresAuth, LocalAuthority: true, ActiveIngestClusterID: object.GetOriginClusterId(),
	}
	if live := object.GetLiveStream(); live != nil {
		resolution.InternalName = object.GetInternalName()
		resolution.IngestMode = live.GetIngestMode()
		resolution.ActiveIngestClusterID = object.GetOriginClusterId()
	}
	if artifact := object.GetArtifact(); artifact != nil {
		resolution.ArtifactHash = artifact.GetArtifactHash()
		resolution.ParentStreamInternalName = artifact.GetParentStreamInternalName()
		if host, _ := state.DefaultManager().FindNodeByArtifactHash(artifact.GetArtifactHash()); host != "" {
			resolution.FixedNode = host
			if p.loadBalancer != nil {
				resolution.FixedNodeID = p.loadBalancer.GetNodeIDByHost(host)
			}
		}
	}
	return resolution, true, err
}

func (p *Processor) EvaluateLocalPlaybackPolicy(ctx context.Context, contentID, internalName string, viewer *ipcpb.ViewerConnectTrigger) (string, bool) {
	local, found, err := p.resolveReadyLocalPlayback(ctx, contentID, true)
	if err != nil {
		if IsLocalAuthorityDenied(err) || errors.Is(err, errLocalAuthorityExpired) {
			return "false", true
		}
		return "", false
	}
	if local.target == nil && !found {
		local, _, err = p.resolveReadyLocalPlayback(ctx, internalName, false)
		if err != nil {
			if IsLocalAuthorityDenied(err) || errors.Is(err, errLocalAuthorityExpired) {
				return "false", true
			}
			return "", false
		}
	}
	if local.target == nil {
		return "", false
	}
	policy, ok := p.localPlaybackPolicyForSnapshot(local.object)
	if !ok {
		return "false", true
	}
	return EvaluatePlaybackPolicyDetailed(ctx, p.logger, internalName, viewer, policy, p.signingKeyUse).MistDecision(), true
}

func (p *Processor) ObserveConnectedPlayback(ctx context.Context, input string, resolution *control.ContentResolution, billing *BillingStatus) {
	if resolution == nil {
		return
	}
	target := &control.StreamTarget{
		InternalName: resolution.RoutingInternalName(), StreamID: resolution.StreamId, TenantID: resolution.TenantId,
		ContentType: resolution.ContentType, ClusterPeers: resolution.ClusterPeers,
		RequiresAuth: resolution.RequiresAuth, RequiresAuthKnown: true,
	}
	info := streamContext{
		TenantID: resolution.TenantId, StreamID: resolution.StreamId, OriginClusterID: resolution.OriginClusterID,
		ClusterPeers: resolution.ClusterPeers, AuthorityClusterPeers: resolution.AuthorityClusterPeers,
	}
	p.promoteLocalPlaybackIfMatching(ctx, input, target, info, billing)
}

// resolveReadyLocalPlayback returns found=true whenever a signed object
// projection exists. A projection is usable only after both it and its tenant
// have passed rollout comparison; an unready pair deliberately falls back to
// the connected path. Once ready, expiry or denial never falls back to a
// central allow.
func (p *Processor) resolveReadyLocalPlayback(ctx context.Context, input string, byPlaybackID bool) (localPlaybackAuthority, bool, error) {
	if p == nil || p.mediaAuthorityStore == nil {
		return localPlaybackAuthority{}, false, nil
	}
	index := "internal_name"
	if byPlaybackID {
		index = "playback_id"
	}
	var object localauthority.MediaObjectSnapshot
	var err error
	if byPlaybackID {
		object, err = p.mediaAuthorityStore.MediaObjectByPlaybackID(ctx, input)
	} else {
		object, err = p.mediaAuthorityStore.MediaObjectByInternalName(ctx, input)
	}
	if errors.Is(err, sql.ErrNoRows) {
		p.observeMediaAuthorityLocalRead(index, "absent")
		return localPlaybackAuthority{}, false, nil
	}
	if err != nil {
		p.observeMediaAuthorityLocalRead(index, "error")
		return localPlaybackAuthority{}, true, fmt.Errorf("read local media-object authority: %w", err)
	}
	result := localPlaybackAuthority{object: object}
	if !object.Ready {
		p.observeMediaAuthorityLocalRead(index, "unready")
		return result, true, nil
	}
	if object.Freshness == localauthority.FreshnessHardExpired {
		p.observeMediaAuthorityLocalRead(index, "hard_expired")
		return result, true, errLocalAuthorityExpired
	}
	payload := object.Authority
	if payload == nil || payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		p.observeMediaAuthorityLocalRead(index, "denied")
		return result, true, errLocalAuthorityDenied
	}
	tenant, err := p.mediaAuthorityStore.Tenant(ctx, payload.GetTenantId())
	if err != nil {
		p.observeMediaAuthorityLocalRead("tenant", "error")
		return result, true, fmt.Errorf("read local tenant authority: %w", err)
	}
	result.tenant = tenant
	if !tenant.Ready {
		p.observeMediaAuthorityLocalRead("tenant", "unready")
		return result, true, nil
	}
	if tenant.Freshness == localauthority.FreshnessHardExpired {
		p.observeMediaAuthorityLocalRead("tenant", "hard_expired")
		return result, true, errLocalAuthorityExpired
	}
	if tenant.Authority == nil || tenant.Authority.GetTenantId() != payload.GetTenantId() {
		return result, true, errors.New("local media and tenant authority identity mismatch")
	}
	if tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		p.observeMediaAuthorityLocalRead("tenant", "denied")
		return result, true, errLocalAuthorityDenied
	}
	result.target = localStreamTarget(payload, tenant.Authority)
	result.info = localStreamContext(payload, tenant.Authority)
	result.target.ClusterPeers = p.mediaAuthorityStore.RoutingClusterPeers(tenant.Authority, p.clusterID)
	result.info.ClusterPeers = p.mediaAuthorityStore.RoutingClusterPeers(tenant.Authority, p.clusterID)
	outcome := "valid"
	if object.Freshness == localauthority.FreshnessSoftExpired || tenant.Freshness == localauthority.FreshnessSoftExpired {
		outcome = "soft_expired"
	}
	p.observeMediaAuthorityLocalRead(index, outcome)
	return result, true, nil
}

func (p *Processor) observeMediaAuthorityLocalRead(index, outcome string) {
	if p != nil && p.metrics != nil && p.metrics.MediaAuthorityLocalReads != nil {
		p.metrics.MediaAuthorityLocalReads.WithLabelValues(index, outcome).Inc()
	}
}

func (p *Processor) observeMediaAuthorityShadow(outcome string) {
	if p != nil && p.metrics != nil && p.metrics.MediaAuthorityShadow != nil {
		p.metrics.MediaAuthorityShadow.WithLabelValues(outcome).Inc()
	}
}

func localStreamTarget(object *mediaauthoritypb.MediaObjectAuthority, tenant *mediaauthoritypb.TenantAuthority) *control.StreamTarget {
	target := &control.StreamTarget{
		TenantID:                    object.GetTenantId(),
		ClusterPeers:                localClusterPeers(tenant),
		OfficialClusterID:           tenant.GetOfficialClusterId(),
		AllowPlatformSharedPlayback: tenant.GetAllowPlatformSharedPlayback(),
		LocalAuthority:              true,
		RequiresAuth:                object.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC,
		RequiresAuthKnown:           true,
	}
	switch object.GetObjectKind() {
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		target.InternalName = control.MistSourceNameForIngestMode(object.GetInternalName(), object.GetLiveStream().GetIngestMode())
		target.StreamID = object.GetLiveStream().GetStreamId()
		target.ContentType = "live"
	case mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		kind := localArtifactKind(object.GetArtifact().GetArtifactKind())
		target.ContentType = kind
		target.StreamID = object.GetArtifact().GetParentStreamId()
		if kind == "dvr" {
			target.InternalName = "dvr+" + object.GetInternalName()
		} else {
			target.InternalName = "vod+" + object.GetInternalName()
			target.IsVod = true
		}
	}
	return target
}

func localStreamContext(object *mediaauthoritypb.MediaObjectAuthority, tenant *mediaauthoritypb.TenantAuthority) streamContext {
	isFree, exhausted := freeTierAllowanceState(tenant.GetAllowances())
	limits := localTenantResourceLimits(tenant, object.GetOriginClusterId())
	info := streamContext{
		TenantID: object.GetTenantId(), UserID: object.GetUserId(), Source: "signed_media_authority",
		OriginClusterID: object.GetOriginClusterId(), OfficialClusterID: tenant.GetOfficialClusterId(),
		ClusterPeers: localClusterPeers(tenant), AuthorityClusterPeers: localClusterPeers(tenant), RequiresAuthKnown: true,
		RequiresAuth: object.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC,
		BillingModel: localBillingModel(tenant.GetBillingModel()), IsFreeTier: isFree, AllowanceExhausted: exhausted,
	}
	if limits != nil {
		info.MaxStreams = limits.GetMaxStreams()
		info.MaxViewers = limits.GetMaxViewers()
	}
	if object.GetLiveStream() != nil {
		info.StreamID = object.GetLiveStream().GetStreamId()
	} else if object.GetArtifact() != nil {
		info.StreamID = object.GetArtifact().GetParentStreamId()
	}
	return info
}

func localBillingStatus(tenant localauthority.TenantSnapshot) *BillingStatus {
	if tenant.Authority == nil || tenant.Freshness == localauthority.FreshnessHardExpired || !tenant.Ready {
		return &BillingStatus{State: BillingStatusUnavailable}
	}
	payload := tenant.Authority
	status := &BillingStatus{TenantID: payload.GetTenantId(), BillingModel: localBillingModel(payload.GetBillingModel())}
	if payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		status.State = BillingStatusDenied
		status.DeniedReason = payload.GetDecisionReason()
		return status
	}
	switch payload.GetBillingDecision() {
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW:
		status.State = BillingStatusHealthy
		if tenant.Freshness == localauthority.FreshnessSoftExpired {
			status.State = BillingStatusStaleValid
		}
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED:
		status.IsBalanceNegative = true
		status.State = BillingStatusDenied
		status.DeniedReason = payload.GetDecisionReason()
	case mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED:
		status.IsSuspended = true
		status.State = BillingStatusDenied
		status.DeniedReason = payload.GetDecisionReason()
	default:
		status.State = BillingStatusDenied
		status.DeniedReason = payload.GetDecisionReason()
	}
	return status
}

func localPlaybackPolicy(object *mediaauthoritypb.MediaObjectAuthority, webhookSecrets ...*mediaauthoritypb.PlaybackWebhookSecret) (*commodorepb.ResolvePlaybackPolicyResponse, bool) {
	if object == nil || object.GetPlaybackPolicy() == nil {
		return nil, false
	}
	policy := object.GetPlaybackPolicy()
	response := &commodorepb.ResolvePlaybackPolicyResponse{TenantId: object.GetTenantId()}
	switch policy.GetKind() {
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC:
		response.Type = "public"
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT:
		response.Type = "jwt"
		response.JwtPolicy = &commodorepb.PlaybackJwtPolicy{
			AllowedKids: policy.GetJwt().GetAllowedKeyIds(), RequiredAudience: policy.GetJwt().GetRequiredAudiences(),
			RequiredClaimsJson: cloneLocalClaims(policy.GetJwt().GetRequiredClaimsJson()),
		}
		for _, key := range policy.GetJwt().GetActiveKeys() {
			response.JwtPolicy.ActiveKeys = append(response.JwtPolicy.ActiveKeys, &commodorepb.PlaybackSigningKey{
				Kid: key.GetKeyId(), Algorithm: key.GetAlgorithm(), PublicKeyPem: key.GetPublicKeyPem(),
			})
		}
	case mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK:
		if policy.GetConnectedOnly() || len(webhookSecrets) == 0 || webhookSecrets[0] == nil {
			return nil, false
		}
		secret := webhookSecrets[0]
		response.Type = "webhook"
		response.WebhookPolicy = &commodorepb.PlaybackWebhookPolicy{Url: secret.GetUrl(), TimeoutMs: secret.GetTimeoutMs(), SecretPt: secret.GetSecret()}
	default:
		return nil, false
	}
	return response, true
}

func (p *Processor) localPlaybackPolicyForSnapshot(snapshot localauthority.MediaObjectSnapshot) (*commodorepb.ResolvePlaybackPolicyResponse, bool) {
	if snapshot.Authority == nil || snapshot.Authority.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK {
		return localPlaybackPolicy(snapshot.Authority)
	}
	if p == nil || p.mediaAuthorityStore == nil {
		return nil, false
	}
	secret, err := p.mediaAuthorityStore.OpenPlaybackWebhookSecret(snapshot)
	if err != nil {
		return nil, false
	}
	return localPlaybackPolicy(snapshot.Authority, secret)
}

func localClusterPeers(tenant *mediaauthoritypb.TenantAuthority) []*clusterpeerpb.TenantClusterPeer {
	if tenant == nil {
		return nil
	}
	peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(tenant.GetEffectiveClusterGrants()))
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant == nil {
			continue
		}
		peer := &clusterpeerpb.TenantClusterPeer{
			ClusterId: grant.GetClusterId(), ClusterClass: grant.GetClusterClass(), DeploymentModel: grant.GetDeploymentModel(),
			OwnerTenantId: grant.GetOwnerTenantId(), AccessLevel: grant.GetAccessLevel(), AccessActive: true,
			SubscriptionStatus: grant.GetSubscriptionStatus(), AccessSource: grant.GetAccessSource(), ResourceLimits: grant.GetResourceLimits(),
			AllowPrivatePullSources: grant.GetAllowPrivatePullSources(), ControlCellId: grant.GetControlCellId(),
			EligibleServingCellIds: append([]string(nil), grant.GetEligibleServingCellIds()...),
		}
		if grant.GetExpiresAt() != nil {
			peer.AccessExpiresAt = grant.GetExpiresAt()
		}
		peers = append(peers, peer)
	}
	return peers
}

func localArtifactKind(kind mediaauthoritypb.ArtifactKind) string {
	switch kind {
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_DVR:
		return "dvr"
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP:
		return "clip"
	case mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CHAPTER:
		return "chapter"
	default:
		return "vod"
	}
}

func localBillingModel(model mediaauthoritypb.TenantBillingModel) string {
	if model == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID {
		return "prepaid"
	}
	return "postpaid"
}

func cloneLocalClaims(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func sameLocalObjectTarget(snapshot localauthority.MediaObjectSnapshot, target *control.StreamTarget) bool {
	if snapshot.Authority == nil || target == nil {
		return false
	}
	want := localStreamTarget(snapshot.Authority, nil)
	return mist.ExtractInternalName(want.InternalName) == mist.ExtractInternalName(target.InternalName) && want.StreamID == target.StreamID &&
		want.TenantID == target.TenantID && want.ContentType == target.ContentType &&
		want.RequiresAuth == target.RequiresAuth && target.RequiresAuthKnown
}

func sameClusterGrantIDs(tenant *mediaauthoritypb.TenantAuthority, peers []*clusterpeerpb.TenantClusterPeer) bool {
	return sharedauthority.SameTenantClusterGrants(tenant, peers) && sharedauthority.SamePreferredCluster(tenant, peers)
}

func (p *Processor) promoteLocalPlaybackIfMatching(ctx context.Context, input string, target *control.StreamTarget, info streamContext, billing *BillingStatus) {
	if p == nil || p.mediaAuthorityStore == nil || target == nil || billing == nil {
		return
	}
	object, err := p.mediaAuthorityStore.MediaObjectByPlaybackID(ctx, input)
	if errors.Is(err, sql.ErrNoRows) && target.InternalName != "" {
		object, err = p.mediaAuthorityStore.MediaObjectByInternalName(ctx, target.InternalName)
	}
	if err != nil || object.Ready || object.Freshness == localauthority.FreshnessHardExpired || !sameLocalObjectTarget(object, target) {
		p.observeMediaAuthorityShadow("object_mismatch")
		return
	}
	localPolicy, locallyEvaluable := p.localPlaybackPolicyForSnapshot(object)
	if !locallyEvaluable || p.commodoreClient == nil {
		p.observeMediaAuthorityShadow("policy_not_comparable")
		return
	}
	policyCtx, policyCancel := context.WithTimeout(ctx, MediaAdmissionTimeout)
	connectedPolicy, policyErr := p.commodoreClient.ResolvePlaybackPolicyByInternalName(policyCtx, object.Authority.GetInternalName())
	policyCancel()
	if policyErr != nil || !sharedauthority.SamePlaybackPolicy(localPolicy, connectedPolicy) {
		p.observeMediaAuthorityShadow("policy_mismatch")
		return
	}
	tenant, err := p.mediaAuthorityStore.Tenant(ctx, target.TenantID)
	if err != nil || tenant.Freshness == localauthority.FreshnessHardExpired || tenant.Authority == nil {
		p.observeMediaAuthorityShadow("tenant_unavailable")
		return
	}
	if tenant.Ready {
		if tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
			tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
			return
		}
		marked, markErr := p.mediaAuthorityStore.MarkMediaObjectLocalReadReady(ctx, object.AuthorityID, object.Version)
		if markErr != nil {
			p.logger.WithError(markErr).WithField("authority_id", object.AuthorityID).Warn("Failed to promote matching local media-object authority")
		} else if marked {
			p.observeMediaAuthorityShadow("object_promoted")
			p.logger.WithFields(logging.Fields{"tenant_id": target.TenantID, "authority_id": object.AuthorityID, "authority_version": object.Version}).Info("Promoted shadow-matched media-object authority to local reads")
		}
		return
	}

	// Resolve the complete connected-mode stream envelope only during shadow
	// promotion. The local path never makes this call, and promotion is fenced
	// to the exact versions compared below.
	if p.commodoreClient != nil {
		shadowCtx, cancel := context.WithTimeout(ctx, MediaAdmissionTimeout)
		resp, resolveErr := p.commodoreClient.ResolveStreamContext(
			shadowCtx, target.StreamID, object.Authority.GetPlaybackId(), object.Authority.GetInternalName(), "",
		)
		cancel()
		if resolveErr == nil && resp.GetAdmitted() && resp.GetTenantId() == target.TenantID {
			limits := resp.GetTenantResourceLimits()
			isFree, exhausted := freeTierAllowanceState(resp.GetAllowances())
			info.OfficialClusterID = resp.GetOfficialClusterId()
			info.ClusterPeers = resp.GetClusterPeers()
			info.AuthorityClusterPeers = resp.GetAuthorityClusterPeers()
			info.IsFreeTier = isFree
			info.AllowanceExhausted = exhausted
			if limits != nil {
				info.MaxStreams = limits.GetMaxStreams()
				info.MaxViewers = limits.GetMaxViewers()
			}
		}
	}
	if mismatch := localTenantDecisionMismatch(tenant.Authority, info, billing); mismatch != "" {
		p.observeMediaAuthorityShadow("tenant_mismatch_" + mismatch)
		return
	}
	marked, err := p.mediaAuthorityStore.MarkPlaybackPairLocalReadReady(ctx, target.TenantID, tenant.Version, object.AuthorityID, object.Version)
	if err != nil {
		p.logger.WithError(err).WithFields(logging.Fields{"tenant_id": target.TenantID, "authority_id": object.AuthorityID}).Warn("Failed to promote matching local media authority")
		return
	}
	if marked {
		p.observeMediaAuthorityShadow("pair_promoted")
		p.logger.WithFields(logging.Fields{"tenant_id": target.TenantID, "authority_id": object.AuthorityID, "authority_version": object.Version}).Info("Promoted shadow-matched media authority to local reads")
	}
}

func localTenantDecisionMismatch(tenant *mediaauthoritypb.TenantAuthority, info streamContext, billing *BillingStatus) string {
	if tenant == nil || billing == nil || tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		return "lifecycle"
	}
	if billing.State == BillingStatusUnavailable || billing.State == BillingStatusDenied || billing.IsSuspended || billing.IsBalanceNegative {
		return "billing_decision"
	}
	if localBillingModel(tenant.GetBillingModel()) != billing.BillingModel {
		return "billing_model"
	}
	if strings.TrimSpace(info.OfficialClusterID) != strings.TrimSpace(tenant.GetOfficialClusterId()) {
		return "official_cluster"
	}
	if !sameClusterGrantIDs(tenant, info.AuthorityClusterPeers) {
		return "cluster_grants"
	}
	limits := sharedauthority.EffectiveResourceLimits(tenant, info.OriginClusterID)
	if limits.GetMaxStreams() != info.MaxStreams || limits.GetMaxViewers() != info.MaxViewers {
		return "resource_limits"
	}
	isFree, exhausted := freeTierAllowanceState(tenant.GetAllowances())
	if isFree != info.IsFreeTier || exhausted != info.AllowanceExhausted {
		return "allowances"
	}
	return ""
}
