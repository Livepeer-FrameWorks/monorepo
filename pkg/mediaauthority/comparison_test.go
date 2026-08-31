package mediaauthority

import (
	"testing"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
)

func TestSameTenantClusterGrantsIgnoresRoutingHealthButChecksAuthority(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{
		ClusterId: "media-a", AccessLevel: "full", SubscriptionStatus: "active",
		ClusterClass: "platform_official", AllowPrivatePullSources: true,
		ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-b", "cell-a"},
	}}}
	peer := &clusterpeerpb.TenantClusterPeer{
		ClusterId: "media-a", AccessLevel: "full", SubscriptionStatus: "active",
		ClusterClass: "platform_official", AllowPrivatePullSources: true,
		ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-a", "cell-b"},
		ClusterSlug: "dynamic-slug", BaseUrl: "https://edge.example", HealthStatus: "degraded",
		S3Endpoint: "https://s3.example",
	}
	if !SameTenantClusterGrants(tenant, []*clusterpeerpb.TenantClusterPeer{peer}) {
		t.Fatal("routing-only fields changed the static authority comparison")
	}
	peer.AllowPrivatePullSources = false
	if SameTenantClusterGrants(tenant, []*clusterpeerpb.TenantClusterPeer{peer}) {
		t.Fatal("authority difference was ignored")
	}
}

func TestSamePreferredClusterIsSeparateFromGrantEquality(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{PreferredClusterId: "media-b"}
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "media-a", Role: "official"}, {ClusterId: "media-b", Role: "preferred"}}
	if !SamePreferredCluster(tenant, peers) {
		t.Fatal("matching preferred route was rejected")
	}
	peers[1].Role = "subscribed"
	if SamePreferredCluster(tenant, peers) {
		t.Fatal("missing preferred route role was ignored")
	}
}

func TestSamePreferredClusterAcceptsNoPreferredRoleSymmetrically(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{ClusterId: "media-a"}}}
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "media-a", Role: "official"}}
	if !SamePreferredCluster(tenant, peers) {
		t.Fatal("two empty preferred roles should compare equal")
	}
	tenant.PreferredClusterId = "filtered-primary"
	if SamePreferredCluster(tenant, peers) {
		t.Fatal("an ungranted preferred id must not compare equal to an absent connected role")
	}
}

func TestSamePreferredClusterFallsBackToOfficialAfterPreferredWasFiltered(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{PreferredClusterId: "official-media"}
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "official-media", Role: "official"}}
	if !SamePreferredCluster(tenant, peers) {
		t.Fatal("signed official fallback did not match the connected official role")
	}
}

func TestSameResourceLimitsTreatsAbsentAsUnlimited(t *testing.T) {
	if !SameResourceLimits(nil, &tenantlimitspb.TenantResourceLimits{}) {
		t.Fatal("nil and zero limits should both represent unlimited")
	}
	if SameResourceLimits(nil, &tenantlimitspb.TenantResourceLimits{MaxViewers: 1}) {
		t.Fatal("bounded and unlimited limits compared equal")
	}
}

func TestSameAllowancesComparesAdmissionDecisionNotLiveCounters(t *testing.T) {
	left := []*meteringpb.MeterAllowance{
		{Meter: " bandwidth ", Included: 100, Used: 40, Remaining: 60, IsFreeTier: true},
		{Meter: "viewers", Included: 10, Used: 2, Remaining: 8},
	}
	right := []*meteringpb.MeterAllowance{
		{Meter: "viewers", Included: 12, Used: 4, Remaining: 8},
		{Meter: "bandwidth", Included: 100, Used: 50, Remaining: 50, IsFreeTier: true},
	}
	if !SameAllowances(left, right) {
		t.Fatal("live counter movement changed the admission decision")
	}
	right[1].Exhausted = true
	if SameAllowances(left, right) {
		t.Fatal("different exhaustion decisions compared equal")
	}
}
