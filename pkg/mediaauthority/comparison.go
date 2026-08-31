package mediaauthority

import (
	"maps"
	"slices"
	"strings"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/protobuf/proto"
)

// SameResourceLimits compares the enforced values, treating an absent message
// and a present all-zero message as the same unlimited decision.
func SameResourceLimits(left, right *tenantlimitspb.TenantResourceLimits) bool {
	return left.GetMaxStreams() == right.GetMaxStreams() && left.GetMaxViewers() == right.GetMaxViewers()
}

// EffectiveResourceLimits applies the grant override for the cluster where the
// decision will be enforced. Zero override fields inherit the tenant base.
func EffectiveResourceLimits(tenant *mediaauthoritypb.TenantAuthority, clusterID string) *tenantlimitspb.TenantResourceLimits {
	if tenant == nil {
		return nil
	}
	base := tenant.GetResourceLimits()
	var override *tenantlimitspb.TenantResourceLimits
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if strings.TrimSpace(grant.GetClusterId()) == strings.TrimSpace(clusterID) {
			override = grant.GetResourceLimits()
			break
		}
	}
	if SameResourceLimits(base, nil) && SameResourceLimits(override, nil) {
		return nil
	}
	out := &tenantlimitspb.TenantResourceLimits{MaxStreams: base.GetMaxStreams(), MaxViewers: base.GetMaxViewers()}
	if override.GetMaxStreams() > 0 {
		out.MaxStreams = override.GetMaxStreams()
	}
	if override.GetMaxViewers() > 0 {
		out.MaxViewers = override.GetMaxViewers()
	}
	return out
}

// SameTenantClusterGrants compares the static entitlement projection shared by
// a signed tenant authority and a connected routing response. Operational
// routing/health fields are intentionally excluded: they are cell-local
// liveness inputs, not tenant authority.
func SameTenantClusterGrants(tenant *mediaauthoritypb.TenantAuthority, peers []*clusterpeerpb.TenantClusterPeer) bool {
	if tenant == nil {
		return false
	}
	want, ok := canonicalGrantSet(tenant.GetEffectiveClusterGrants())
	if !ok {
		return false
	}
	got := make(map[string]*mediaauthoritypb.TenantClusterGrant, len(peers))
	for _, peer := range peers {
		grant := grantFromPeer(peer)
		if grant == nil || got[grant.GetClusterId()] != nil {
			return false
		}
		got[grant.GetClusterId()] = grant
	}
	return maps.EqualFunc(want, got, func(left, right *mediaauthoritypb.TenantClusterGrant) bool {
		return proto.Equal(left, right)
	})
}

// SamePreferredCluster compares the stable tenant route role separately from
// the grant set. Role is not part of a TenantClusterGrant, but it materially
// changes always-on federation behavior and local routing priority.
func SamePreferredCluster(tenant *mediaauthoritypb.TenantAuthority, peers []*clusterpeerpb.TenantClusterPeer) bool {
	if tenant == nil {
		return false
	}
	want := strings.TrimSpace(tenant.GetPreferredClusterId())
	got := ""
	official := ""
	for _, peer := range peers {
		if peer == nil {
			continue
		}
		switch {
		case strings.EqualFold(strings.TrimSpace(peer.GetRole()), "preferred"):
			if got != "" {
				return false
			}
			got = strings.TrimSpace(peer.GetClusterId())
		case strings.EqualFold(strings.TrimSpace(peer.GetRole()), "official"):
			if official != "" {
				return false
			}
			official = strings.TrimSpace(peer.GetClusterId())
		}
	}
	if got == "" && want != "" {
		got = official
	}
	return want == got
}

func canonicalGrantSet(grants []*mediaauthoritypb.TenantClusterGrant) (map[string]*mediaauthoritypb.TenantClusterGrant, bool) {
	out := make(map[string]*mediaauthoritypb.TenantClusterGrant, len(grants))
	for _, raw := range grants {
		if raw == nil {
			return nil, false
		}
		grant := proto.CloneOf(raw)
		grant.ClusterId = strings.TrimSpace(grant.GetClusterId())
		if grant.ClusterId == "" || out[grant.ClusterId] != nil {
			return nil, false
		}
		grant.AccessLevel = strings.TrimSpace(grant.GetAccessLevel())
		grant.SubscriptionStatus = strings.TrimSpace(grant.GetSubscriptionStatus())
		grant.ClusterClass = strings.TrimSpace(grant.GetClusterClass())
		grant.DeploymentModel = strings.TrimSpace(grant.GetDeploymentModel())
		grant.OwnerTenantId = strings.TrimSpace(grant.GetOwnerTenantId())
		grant.ControlCellId = strings.TrimSpace(grant.GetControlCellId())
		grant.EligibleServingCellIds = sortedUniqueStrings(grant.GetEligibleServingCellIds())
		if SameResourceLimits(grant.GetResourceLimits(), nil) {
			grant.ResourceLimits = nil
		}
		out[grant.ClusterId] = grant
	}
	return out, true
}

func grantFromPeer(peer *clusterpeerpb.TenantClusterPeer) *mediaauthoritypb.TenantClusterGrant {
	if peer == nil || strings.TrimSpace(peer.GetClusterId()) == "" {
		return nil
	}
	grant := &mediaauthoritypb.TenantClusterGrant{
		ClusterId: strings.TrimSpace(peer.GetClusterId()), AccessSource: peer.GetAccessSource(),
		AccessLevel: strings.TrimSpace(peer.GetAccessLevel()), SubscriptionStatus: strings.TrimSpace(peer.GetSubscriptionStatus()),
		ClusterClass: strings.TrimSpace(peer.GetClusterClass()), DeploymentModel: strings.TrimSpace(peer.GetDeploymentModel()),
		OwnerTenantId: strings.TrimSpace(peer.GetOwnerTenantId()), AllowPrivatePullSources: peer.GetAllowPrivatePullSources(),
		ControlCellId: strings.TrimSpace(peer.GetControlCellId()), EligibleServingCellIds: sortedUniqueStrings(peer.GetEligibleServingCellIds()),
	}
	if peer.GetAccessExpiresAt() != nil {
		grant.ExpiresAt = proto.CloneOf(peer.GetAccessExpiresAt())
	}
	if !SameResourceLimits(peer.GetResourceLimits(), nil) {
		grant.ResourceLimits = proto.CloneOf(peer.GetResourceLimits())
	}
	return grant
}

// SameAllowances compares the two decisions the media load gate consumes.
// Included/used/remaining are telemetry snapshots and legitimately move
// between bundle mint and a connected shadow read; they are not promotion
// authority. A change to free-tier identity or exhaustion remains material.
func SameAllowances(left, right []*meteringpb.MeterAllowance) bool {
	decision := func(values []*meteringpb.MeterAllowance) (free, exhausted bool) {
		for _, allowance := range values {
			if allowance == nil || !allowance.GetIsFreeTier() {
				continue
			}
			free = true
			exhausted = exhausted || allowance.GetExhausted()
		}
		return free, exhausted
	}
	leftFree, leftExhausted := decision(left)
	rightFree, rightExhausted := decision(right)
	return leftFree == rightFree && leftExhausted == rightExhausted
}

// SamePlaybackPolicy compares policy decisions after normalizing every
// repeated field whose ordering is not semantically meaningful.
func SamePlaybackPolicy(left, right *commodorepb.ResolvePlaybackPolicyResponse) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	want, got := proto.CloneOf(left), proto.CloneOf(right)
	normalizePlaybackPolicy(want)
	normalizePlaybackPolicy(got)
	return proto.Equal(want, got)
}

func normalizePlaybackPolicy(policy *commodorepb.ResolvePlaybackPolicyResponse) {
	jwt := policy.GetJwtPolicy()
	if jwt == nil {
		return
	}
	jwt.AllowedKids = sortedUniqueStrings(jwt.GetAllowedKids())
	jwt.RequiredAudience = sortedUniqueStrings(jwt.GetRequiredAudience())
	slices.SortFunc(jwt.ActiveKeys, func(left, right *commodorepb.PlaybackSigningKey) int {
		if order := strings.Compare(left.GetKid(), right.GetKid()); order != 0 {
			return order
		}
		if order := strings.Compare(left.GetAlgorithm(), right.GetAlgorithm()); order != 0 {
			return order
		}
		return strings.Compare(left.GetPublicKeyPem(), right.GetPublicKeyPem())
	})
}

func sortedUniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
