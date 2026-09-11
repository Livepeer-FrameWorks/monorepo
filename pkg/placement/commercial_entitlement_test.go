package placement

import (
	"testing"
	"time"

	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCommercialEntitlementDigestBindsPermissionNotPresentation(t *testing.T) {
	const tenant = "81000000-0000-4000-8000-000000000001"
	peer := &clusterpb.TenantClusterPeer{ClusterId: "a", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, MediaConsent: &pb.CapacityConsent{Revision: 2, AllowIngest: true, AllowServe: true, AllowExternalSource: true}}
	other := proto.CloneOf(peer)
	other.ClusterId = "z"
	baseline, err := CommercialEntitlementDigest(tenant, []*clusterpb.TenantClusterPeer{peer, other})
	if err != nil {
		t.Fatal(err)
	}
	changed := proto.CloneOf(peer)
	changed.HealthStatus = "offline"
	same, err := CommercialEntitlementDigest(tenant, []*clusterpb.TenantClusterPeer{other, changed})
	if err != nil || same != baseline || peer.HealthStatus != "" {
		t.Fatal("ordering/health changed permission or caller state")
	}
	for name, mutate := range map[string]func(*clusterpb.TenantClusterPeer){
		"owner":            func(p *clusterpb.TenantClusterPeer) { p.OwnerTenantId = tenant },
		"consent revision": func(p *clusterpb.TenantClusterPeer) { p.MediaConsent.Revision++ },
		"consent ingest":   func(p *clusterpb.TenantClusterPeer) { p.MediaConsent.AllowIngest = false },
		"consent serve":    func(p *clusterpb.TenantClusterPeer) { p.MediaConsent.AllowServe = false },
		"consent external": func(p *clusterpb.TenantClusterPeer) { p.MediaConsent.AllowExternalSource = false },
		"expiry":           func(p *clusterpb.TenantClusterPeer) { p.AccessExpiresAt = timestamppb.New(time.Unix(1800000000, 0)) },
		"source": func(p *clusterpb.TenantClusterPeer) {
			p.AccessSource = clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OPERATOR_OVERRIDE
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := proto.CloneOf(peer)
			mutate(value)
			digest, err := CommercialEntitlementDigest(tenant, []*clusterpb.TenantClusterPeer{value, other})
			if err != nil || digest == baseline {
				t.Fatalf("permission not bound: %v", err)
			}
		})
	}
	for _, invalid := range [][]*clusterpb.TenantClusterPeer{nil, {nil}, {peer, peer}, {{}}, make([]*clusterpb.TenantClusterPeer, 4097)} {
		if _, err := CommercialEntitlementDigest(tenant, invalid); err == nil {
			t.Fatal("ambiguous permission accepted")
		}
	}
}
