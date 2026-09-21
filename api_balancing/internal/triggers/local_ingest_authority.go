package triggers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
)

type localIngestAuthority struct {
	object localauthority.MediaObjectSnapshot
	tenant localauthority.TenantSnapshot
}

// ResolveLocalIngestContext exposes the signed credential projection to the
// public ingest endpoint resolvers without taking a placement claim. A ready
// denial or hard-expired marker is handled locally; an unready shadow remains
// on the connected path.
func (p *Processor) ResolveLocalIngestContext(ctx context.Context, credential string) (*commodorepb.ResolveStreamContextResponse, bool, error) {
	validation, local, found, err := p.resolveReadyLocalIngest(ctx, credential)
	if err != nil {
		return nil, found, err
	}
	if !found || validation == nil {
		return nil, false, nil
	}
	response := &commodorepb.ResolveStreamContextResponse{
		Admitted: validation.GetValid(), AdmissionReason: validation.GetError(), RejectionReason: validation.GetRejectionReason(),
		StreamId: validation.GetStreamId(), PlaybackId: validation.GetPlaybackId(), InternalName: validation.GetInternalName(),
		IngestMode: "push", TenantId: validation.GetTenantId(), UserId: validation.GetUserId(),
		IsRecordingEnabled: validation.GetIsRecordingEnabled(), BillingModel: validation.GetBillingModel(),
		IsSuspended: validation.GetIsSuspended(), IsBalanceNegative: validation.GetIsBalanceNegative(),
		OriginClusterId: validation.OriginClusterId, OfficialClusterId: validation.OfficialClusterId,
		ClusterPeers: validation.GetClusterPeers(), AuthorityClusterPeers: validation.GetAuthorityClusterPeers(), ProcessesJson: validation.GetProcessesJson(),
		DvrProcessesJson: validation.GetDvrProcessesJson(), DvrPolicy: validation.GetDvrPolicy(),
		Allowances: validation.GetAllowances(), TenantResourceLimits: validation.GetTenantResourceLimits(),
	}
	if local.object.Authority != nil {
		response.RequiresAuth = local.object.Authority.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC
	}
	return response, true, nil
}

// resolveReadyLocalIngest performs the credential-indexed half of
// PUSH_REWRITE admission. found means a signed object exists; a nil response
// means that object has not passed connected shadow comparison yet. Once the
// exact versions are marked ready, denial or hard expiry never falls back to a
// central allow.
//
// An authority held past its validity is asked for once and read again; when
// that cannot be done the expiry stands and the push is refused. A credential
// the cell holds nothing for is not fetched here: the credential never leaves
// the cell, so the caller validates it against the control plane, which names
// the stream, and asks for that stream's authority (fetchLocalIngestAuthority).
func (p *Processor) resolveReadyLocalIngest(ctx context.Context, credential string) (*commodorepb.ValidateStreamKeyResponse, localIngestAuthority, bool, error) {
	response, result, found, err := p.readReadyLocalIngest(ctx, credential)
	if IsLocalAuthorityExpired(err) && result.object.AuthorityID != "" &&
		p.fetchLocalAuthority(ctx, localauthority.AuthorityLookup{AuthorityID: result.object.AuthorityID}) {
		response, result, found, err = p.readReadyLocalIngest(ctx, credential)
	}
	if err == nil && response != nil && response.GetValid() {
		p.mediaAuthorityStore.NoteMediaObjectUse(result.object)
	}
	return response, result, found, err
}

// fetchLocalIngestAuthority brings a stream's authority into this cell after the
// control plane validated its key. Final placement admission decides on the
// local pair alone, so a stream nobody has used for a while can only be admitted
// once its authority is here.
func (p *Processor) fetchLocalIngestAuthority(ctx context.Context, streamID string) {
	if streamID = strings.TrimSpace(streamID); streamID != "" {
		p.fetchLocalAuthority(ctx, localauthority.AuthorityLookup{AuthorityID: sharedauthority.LiveStreamAuthorityID(streamID)})
	}
}

func (p *Processor) readReadyLocalIngest(ctx context.Context, credential string) (*commodorepb.ValidateStreamKeyResponse, localIngestAuthority, bool, error) {
	if p == nil || p.mediaAuthorityStore == nil {
		return nil, localIngestAuthority{}, false, nil
	}
	object, err := p.mediaAuthorityStore.MediaObjectByPublishingCredential(ctx, credential)
	if errors.Is(err, sql.ErrNoRows) {
		p.observeMediaAuthorityLocalRead("publishing_credential", "absent")
		return nil, localIngestAuthority{}, false, nil
	}
	if err != nil {
		p.observeMediaAuthorityLocalRead("publishing_credential", "error")
		return nil, localIngestAuthority{}, true, fmt.Errorf("read local publishing authority: %w", err)
	}
	result := localIngestAuthority{object: object}
	if !object.IngestReady {
		p.observeMediaAuthorityLocalRead("publishing_credential", "unready")
		return nil, result, true, nil
	}
	payload := object.Authority
	live := payload.GetLiveStream()
	// A tombstone is terminal however old it is, and it is never renewed, so it
	// is refused here before its age can turn the refusal into a question for
	// the control plane.
	if payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		p.observeMediaAuthorityLocalRead("publishing_credential", "denied")
		return &commodorepb.ValidateStreamKeyResponse{
			Valid: false, Error: "Invalid stream key", TenantId: payload.GetTenantId(), UserId: payload.GetUserId(),
			StreamId: live.GetStreamId(), PlaybackId: payload.GetPlaybackId(), InternalName: payload.GetInternalName(),
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
		}, result, true, nil
	}
	if object.Freshness == localauthority.FreshnessHardExpired {
		p.observeMediaAuthorityLocalRead("publishing_credential", "hard_expired")
		return nil, result, true, errLocalAuthorityExpired
	}
	if payload != nil && live != nil && payload.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && live.GetIngestMode() != "push" {
		p.observeMediaAuthorityLocalRead("publishing_credential", "denied")
		return &commodorepb.ValidateStreamKeyResponse{
			Valid: false, Error: "This source mode does not accept push ingest", TenantId: payload.GetTenantId(), UserId: payload.GetUserId(),
			StreamId: live.GetStreamId(), PlaybackId: payload.GetPlaybackId(), InternalName: payload.GetInternalName(),
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_NON_PUSH_MODE,
		}, result, true, nil
	}
	if payload == nil || live == nil || payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		!sharedauthority.VerifyPublishingCredential(credential, live.GetPublishingCredentialSha256()) {
		p.observeMediaAuthorityLocalRead("publishing_credential", "denied")
		return &commodorepb.ValidateStreamKeyResponse{
			Valid: false, Error: "Invalid stream key", TenantId: payload.GetTenantId(), UserId: payload.GetUserId(),
			StreamId: live.GetStreamId(), PlaybackId: payload.GetPlaybackId(), InternalName: payload.GetInternalName(),
			RejectionReason: commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY,
		}, result, true, nil
	}
	tenant, err := p.mediaAuthorityStore.TenantForObject(ctx, object)
	if err != nil {
		return nil, result, true, fmt.Errorf("read local tenant ingest authority: %w", err)
	}
	result.tenant = tenant
	if !tenant.IngestReady {
		p.observeMediaAuthorityLocalRead("tenant_ingest", "unready")
		return nil, result, true, nil
	}
	if tenant.Freshness == localauthority.FreshnessHardExpired {
		p.observeMediaAuthorityLocalRead("tenant_ingest", "hard_expired")
		return nil, result, true, errLocalAuthorityExpired
	}
	tenantPayload := tenant.Authority
	if tenantPayload == nil || tenantPayload.GetTenantId() != payload.GetTenantId() {
		return nil, result, true, errors.New("local stream and tenant ingest authority identity mismatch")
	}
	if tenantPayload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenantPayload.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		reason := commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE
		if tenantPayload.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED {
			reason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED
		} else if tenantPayload.GetBillingDecision() == mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED {
			reason = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE
		}
		return &commodorepb.ValidateStreamKeyResponse{
			Valid: false, Error: tenantPayload.GetDecisionReason(), RejectionReason: reason,
			TenantId: payload.GetTenantId(), UserId: payload.GetUserId(), StreamId: live.GetStreamId(),
			PlaybackId: payload.GetPlaybackId(), InternalName: payload.GetInternalName(),
		}, result, true, nil
	}

	origin := payload.GetOriginClusterId()
	official := tenantPayload.GetOfficialClusterId()
	response := &commodorepb.ValidateStreamKeyResponse{
		Valid: true, UserId: payload.GetUserId(), TenantId: payload.GetTenantId(), InternalName: payload.GetInternalName(),
		IsRecordingEnabled: live.GetRecordingEnabled(), StreamId: live.GetStreamId(), PlaybackId: payload.GetPlaybackId(),
		BillingModel: localBillingModel(tenantPayload.GetBillingModel()), OriginClusterId: &origin, OfficialClusterId: &official,
		ClusterPeers: p.mediaAuthorityStore.RoutingClusterPeers(tenantPayload, p.clusterID), AuthorityClusterPeers: localClusterPeers(tenantPayload), ProcessesJson: live.GetProcessesJson(), DvrProcessesJson: live.GetDvrProcessesJson(),
		DvrPolicy: tenantPayload.GetDvrPolicy(), Allowances: tenantPayload.GetAllowances(), TenantResourceLimits: localTenantResourceLimits(tenantPayload, live.GetOutageIngestClusterId()),
	}
	secret, secretErr := p.mediaAuthorityStore.OpenLiveStreamSecret(object)
	if secretErr != nil && len(live.GetSealedCellSecrets()) > 0 {
		return nil, result, true, fmt.Errorf("open local ingest output authority: %w", secretErr)
	}
	for _, target := range secret.GetPushTargets() {
		response.PushTargets = append(response.PushTargets, &commodorepb.PushTargetInternal{
			Id: target.GetTargetId(), Platform: target.GetPlatform(), Name: target.GetName(), TargetUri: target.GetTargetUri(),
		})
	}
	outcome := "valid"
	if object.Freshness == localauthority.FreshnessSoftExpired || tenant.Freshness == localauthority.FreshnessSoftExpired {
		outcome = "soft_expired"
	}
	p.observeMediaAuthorityLocalRead("publishing_credential", outcome)
	return response, result, true, nil
}

func localTenantResourceLimits(tenant *mediaauthoritypb.TenantAuthority, clusterID string) *tenantlimitspb.TenantResourceLimits {
	return sharedauthority.EffectiveResourceLimits(tenant, clusterID)
}

func localOutageIngestAllowed(local localIngestAuthority, ingestClusterID string) bool {
	if local.object.Authority == nil || local.tenant.Authority == nil {
		return false
	}
	owner := strings.TrimSpace(local.object.Authority.GetLiveStream().GetOutageIngestClusterId())
	if owner == "" || owner != strings.TrimSpace(ingestClusterID) {
		return false
	}
	for _, grant := range local.tenant.Authority.GetEffectiveClusterGrants() {
		if strings.TrimSpace(grant.GetClusterId()) == owner {
			return true
		}
	}
	return false
}

func (p *Processor) promoteLocalIngestIfMatching(ctx context.Context, credential, ingestClusterID string, connected *commodorepb.ValidateStreamKeyResponse) {
	if p == nil || p.mediaAuthorityStore == nil || connected == nil || !connected.GetValid() {
		return
	}
	object, err := p.mediaAuthorityStore.MediaObjectByPublishingCredential(ctx, credential)
	if err != nil || object.IngestReady || object.Freshness == localauthority.FreshnessHardExpired || object.Authority == nil {
		return
	}
	if object.Authority.GetSchemaVersion() != sharedauthority.SchemaVersion {
		p.observeMediaAuthorityShadow("ingest_object_mismatch")
		return
	}
	live := object.Authority.GetLiveStream()
	if live == nil || !sharedauthority.VerifyPublishingCredential(credential, live.GetPublishingCredentialSha256()) ||
		object.Authority.GetTenantId() != connected.GetTenantId() || object.Authority.GetUserId() != connected.GetUserId() ||
		object.Authority.GetInternalName() != connected.GetInternalName() || object.Authority.GetPlaybackId() != connected.GetPlaybackId() ||
		live.GetStreamId() != connected.GetStreamId() || live.GetRecordingEnabled() != connected.GetIsRecordingEnabled() ||
		live.GetProcessesJson() != connected.GetProcessesJson() || live.GetDvrProcessesJson() != connected.GetDvrProcessesJson() ||
		strings.TrimSpace(object.Authority.GetOriginClusterId()) != strings.TrimSpace(connected.GetOriginClusterId()) {
		p.observeMediaAuthorityShadow("ingest_object_mismatch")
		return
	}
	secret, secretErr := p.mediaAuthorityStore.OpenLiveStreamSecret(object)
	if secretErr != nil && len(live.GetSealedCellSecrets()) > 0 {
		p.observeMediaAuthorityShadow("ingest_output_unavailable")
		return
	}
	if !samePushTargets(secret.GetPushTargets(), connected.GetPushTargets()) {
		p.observeMediaAuthorityShadow("ingest_output_mismatch")
		return
	}
	tenant, err := p.mediaAuthorityStore.TenantForObject(ctx, object)
	if err != nil || tenant.Freshness == localauthority.FreshnessHardExpired || tenant.Authority == nil {
		return
	}
	if !localauthority.ShadowComparable(tenant.Authority, object.Authority) {
		p.observeMediaAuthorityShadow("ingest_tenant_mismatch")
		return
	}
	if !sameLocalTenantIngestDecision(tenant.Authority, ingestClusterID, connected) {
		p.observeMediaAuthorityShadow("ingest_tenant_mismatch")
		return
	}
	var marked bool
	if tenant.IngestReady {
		marked, err = p.mediaAuthorityStore.MarkMediaObjectLocalIngestReady(ctx, connected.GetTenantId(), object.AuthorityID, object.Version)
	} else {
		marked, err = p.mediaAuthorityStore.MarkIngestPairLocalReady(ctx, connected.GetTenantId(), tenant.Version, object.AuthorityID, object.Version)
	}
	if err != nil {
		p.logger.WithError(err).WithField("authority_id", object.AuthorityID).Warn("Failed to promote matching local ingest authority")
		return
	}
	if marked {
		p.observeMediaAuthorityShadow("ingest_pair_promoted")
	}
}

func samePushTargets(local []*mediaauthoritypb.PushTargetSecret, connected []*commodorepb.PushTargetInternal) bool {
	if len(local) != len(connected) {
		return false
	}
	byID := make(map[string]*commodorepb.PushTargetInternal, len(connected))
	for _, target := range connected {
		if target == nil || strings.TrimSpace(target.GetId()) == "" || byID[target.GetId()] != nil {
			return false
		}
		byID[target.GetId()] = target
	}
	for _, target := range local {
		candidate := byID[target.GetTargetId()]
		if target == nil || candidate == nil || target.GetTargetUri() != candidate.GetTargetUri() || target.GetName() != candidate.GetName() || target.GetPlatform() != candidate.GetPlatform() {
			return false
		}
	}
	return true
}

func sameLocalTenantIngestDecision(tenant *mediaauthoritypb.TenantAuthority, clusterID string, connected *commodorepb.ValidateStreamKeyResponse) bool {
	if tenant == nil || connected == nil || tenant.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW || connected.GetIsSuspended() || connected.GetIsBalanceNegative() ||
		localBillingModel(tenant.GetBillingModel()) != connected.GetBillingModel() || strings.TrimSpace(tenant.GetOfficialClusterId()) != strings.TrimSpace(connected.GetOfficialClusterId()) ||
		!sharedauthority.SameTenantClusterGrants(tenant, connected.GetAuthorityClusterPeers()) || !sharedauthority.SamePreferredCluster(tenant, connected.GetAuthorityClusterPeers()) ||
		!sharedauthority.SameResourceLimits(localTenantResourceLimits(tenant, clusterID), connected.GetTenantResourceLimits()) ||
		!proto.Equal(tenant.GetDvrPolicy(), connected.GetDvrPolicy()) {
		return false
	}
	return sharedauthority.SameAllowances(tenant.GetAllowances(), connected.GetAllowances())
}
