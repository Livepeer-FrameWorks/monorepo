package control

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

var localMediaAuthorityStore atomic.Pointer[localauthority.Store]

func SetLocalMediaAuthorityStore(store *localauthority.Store) {
	localMediaAuthorityStore.Store(store)
}

func LocalTenantAllowsSourceCluster(tenant *mediaauthoritypb.TenantAuthority, clusterID string, private bool) bool {
	clusterID = strings.TrimSpace(clusterID)
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant.GetClusterId() == clusterID {
			return !private || grant.GetAllowPrivatePullSources()
		}
	}
	return false
}

func LocalMediaAuthorityStore() *localauthority.Store {
	return localMediaAuthorityStore.Load()
}

type LocalPullSourceResolution struct {
	Response *commodorepb.ResolvePullSourceByInternalNameResponse
	Snapshot localauthority.SourceSnapshot
	Found    bool
	Marked   bool
}

// ResolveLocalPullSource reports unmarked signed state separately from marked
// state. Once both projections are marked, callers must never fall back to a
// central answer for denial, expiry, corruption, or missing sealed material.
func ResolveLocalPullSource(ctx context.Context, store *localauthority.Store, internalName string) (LocalPullSourceResolution, error) {
	if store == nil {
		return LocalPullSourceResolution{}, nil
	}
	snapshot, found, err := store.PullSource(ctx, internalName)
	result := LocalPullSourceResolution{Snapshot: snapshot, Found: found}
	if !found {
		return result, nil
	}
	// Source rollout is object-scoped. Tenant readiness is a prerequisite shared
	// by every object owned by that tenant, so tenant-ready/object-unready is the
	// normal state for a newly created second object rather than corruption.
	result.Marked = snapshot.Object.SourceReady
	if snapshot.Object.SourceReady && !snapshot.Tenant.SourceReady {
		return result, errors.New("local source object is ready before its tenant")
	}
	if !result.Marked {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if snapshot.Object.Freshness == localauthority.FreshnessHardExpired || snapshot.Tenant.Freshness == localauthority.FreshnessHardExpired {
		return result, errors.New("local source authority hard-expired")
	}
	object := snapshot.Object.Authority
	secret := snapshot.Secret
	result.Response = &commodorepb.ResolvePullSourceByInternalNameResponse{
		Found: true, Enabled: localauthority.SourceDecisionAllows(snapshot),
		TenantId: object.GetTenantId(), StreamId: object.GetLiveStream().GetStreamId(),
	}
	if secret != nil {
		result.Response.SourceUri = secret.GetSourceUri()
		result.Response.AllowedClusterIds = append([]string(nil), secret.GetAllowedClusterIds()...)
	}
	return result, nil
}

type PullSourcePromotion string

const (
	PullSourcePromotionNone     PullSourcePromotion = ""
	PullSourcePromotionMatched  PullSourcePromotion = "matched"
	PullSourcePromotionMismatch PullSourcePromotion = "mismatch"
)

func PromoteLocalPullSourceIfMatching(ctx context.Context, store *localauthority.Store, connected *commodorepb.ResolvePullSourceByInternalNameResponse, streamCtx *commodorepb.ResolveStreamContextResponse, snapshot localauthority.SourceSnapshot) (PullSourcePromotion, error) {
	if store == nil || connected == nil || !connected.GetFound() || streamCtx == nil || !streamCtx.GetAdmitted() || snapshot.Secret == nil ||
		snapshot.Object.Authority == nil || snapshot.Tenant.Authority == nil || snapshot.Object.SourceReady {
		return PullSourcePromotionNone, nil
	}
	object := snapshot.Object.Authority
	wantAllowed := append([]string(nil), snapshot.Secret.GetAllowedClusterIds()...)
	gotAllowed := append([]string(nil), connected.GetAllowedClusterIds()...)
	slices.Sort(wantAllowed)
	slices.Sort(gotAllowed)
	if object.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		snapshot.Tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		snapshot.Tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW ||
		object.GetTenantId() != connected.GetTenantId() || object.GetLiveStream().GetStreamId() != connected.GetStreamId() ||
		snapshot.Secret.GetSourceUri() != connected.GetSourceUri() || snapshot.Secret.GetSourceEnabled() != connected.GetEnabled() ||
		!slices.Equal(wantAllowed, gotAllowed) || !sameLocalSourceTenant(snapshot.Tenant.Authority, object.GetOriginClusterId(), streamCtx) {
		return PullSourcePromotionMismatch, nil
	}
	var marked bool
	var err error
	if snapshot.Tenant.SourceReady {
		marked, err = store.MarkMediaObjectLocalSourceReady(ctx, snapshot.Object.AuthorityID, snapshot.Object.Version)
	} else {
		marked, err = store.MarkSourcePairLocalReady(ctx, object.GetTenantId(), snapshot.Tenant.Version, snapshot.Object.AuthorityID, snapshot.Object.Version)
	}
	if err != nil {
		return PullSourcePromotionNone, err
	}
	if marked {
		return PullSourcePromotionMatched, nil
	}
	return PullSourcePromotionNone, nil
}

func sameLocalSourceTenant(tenant *mediaauthoritypb.TenantAuthority, clusterID string, streamCtx *commodorepb.ResolveStreamContextResponse) bool {
	if tenant == nil || streamCtx == nil || !streamCtx.GetAdmitted() || tenant.GetTenantId() != streamCtx.GetTenantId() ||
		localBillingModel(tenant.GetBillingModel()) != streamCtx.GetBillingModel() || tenant.GetOfficialClusterId() != streamCtx.GetOfficialClusterId() ||
		!sharedauthority.SameResourceLimits(sharedauthority.EffectiveResourceLimits(tenant, clusterID), streamCtx.GetTenantResourceLimits()) ||
		!sharedauthority.SameTenantClusterGrants(tenant, streamCtx.GetAuthorityClusterPeers()) ||
		!sharedauthority.SamePreferredCluster(tenant, streamCtx.GetAuthorityClusterPeers()) ||
		!sharedauthority.SameAllowances(tenant.GetAllowances(), streamCtx.GetAllowances()) {
		return false
	}
	return true
}

func localBillingModel(model mediaauthoritypb.TenantBillingModel) string {
	if model == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID {
		return "prepaid"
	}
	return "postpaid"
}
