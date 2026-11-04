package balancer

import (
	"errors"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

// laggingPlacementFixture is the cell right after a tenant placement change:
// the tenant authority it holds is at placement revision 4, which serves only
// from platform-official capacity and also withdrew the "own" grant, while the
// stream's object authority is still the one compiled against revision 3,
// which the cell held until a second ago.
func laggingPlacementFixture() (localauthority.PlacementPair, time.Time) {
	pair, now := placementAuthorityFixture()
	previous := proto.CloneOf(pair.Tenant.Authority.GetMediaPlacement())
	pair.Tenant.Authority.MediaPlacement = &placementpb.PolicySet{Revision: 4, Serve: &placementpb.Rules{SchemaVersion: 1,
		Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}}}}}}
	grants := pair.Tenant.Authority.GetEffectiveClusterGrants()
	pair.Tenant.Authority.EffectiveClusterGrants = []*mediaauthoritypb.TenantClusterGrant{grants[0], grants[2]}
	pair.Tenant.PreviousPlacement = previous
	pair.Tenant.PlacementSupersededAt = now.Add(-time.Second)
	return pair, now
}

// While the stream's recompiled authority is on its way, the cell resolves it
// under the policy it was compiled for instead of failing resolution, and the
// newer tenant's other facts still apply.
func TestPlacementAuthorityServesLaggingObjectOnPreviousTenantRevision(t *testing.T) {
	consistent, now := placementAuthorityFixture()
	want, err := CompilePlacementAuthority(consistent, placement.Serve, now)
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := laggingPlacementFixture()
	held := proto.CloneOf(pair.Tenant.Authority)
	got, err := CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil {
		t.Fatalf("tenant at placement revision 4 with the object still at parent revision 3: %v, want the revision-3 policy", err)
	}
	if got.ParentRevision != 3 || got.PolicyRevision != 5 || got.PolicyDigest != want.PolicyDigest {
		t.Fatalf("decided under parent %d policy %d digest %s, want parent 3 policy 5 digest %s", got.ParentRevision, got.PolicyRevision, got.PolicyDigest, want.PolicyDigest)
	}
	if _, kept := got.Clusters["own"]; kept || len(got.Clusters) != 2 || got.TenantAuthorityVersion != pair.Tenant.Version {
		t.Fatalf("lagging object must keep the held tenant's grants (own withdrawn): clusters %v tenant version %d", got.Clusters, got.TenantAuthorityVersion)
	}
	if limit := pair.Tenant.PlacementSupersededAt.Add(localauthority.PlacementParentLagWindow); got.ExpiresAt.After(limit) {
		t.Fatalf("decision under the previous revision expires %s, after the lag window ends %s", got.ExpiresAt, limit)
	}
	if !proto.Equal(pair.Tenant.Authority, held) {
		t.Fatal("compiling a lagging object changed the held tenant authority")
	}
}

// The fence still refuses every mismatch that is not an object catching up
// with the tenant revision the cell held just before.
func TestPlacementAuthorityRefusesParentRevisionsOutsideTheLag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*localauthority.PlacementPair, time.Time)
	}{
		{"object_names_a_revision_the_cell_never_held", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Object.Authority.PlacementTenantRevision = 2
		}},
		{"object_ahead_of_the_tenant", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Object.Authority.PlacementTenantRevision = 5
		}},
		{"lag_outlived_its_window", func(p *localauthority.PlacementPair, now time.Time) {
			p.Tenant.PlacementSupersededAt = now.Add(-localauthority.PlacementParentLagWindow)
		}},
		{"no_previous_revision_held", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.PreviousPlacement = nil
		}},
		{"previous_revision_without_a_replacement_time", func(p *localauthority.PlacementPair, _ time.Time) {
			p.Tenant.PlacementSupersededAt = time.Time{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pair, now := laggingPlacementFixture()
			tc.mutate(&pair, now)
			for _, verb := range []placement.Verb{placement.Serve, placement.Ingest} {
				if _, err := CompilePlacementAuthority(pair, verb, now); !errors.Is(err, ErrPlacementAuthorityInvalid) {
					t.Fatalf("%s: got %v, want %v", verb, err, ErrPlacementAuthorityInvalid)
				}
			}
		})
	}
}

// An object that already names the held revision is evaluated against it,
// whatever the cell kept of the previous one.
func TestPlacementAuthorityCaughtUpObjectUsesHeldRevision(t *testing.T) {
	pair, now := laggingPlacementFixture()
	pair.Object.Authority.PlacementTenantRevision = 4
	got, err := CompilePlacementAuthority(pair, placement.Serve, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentRevision != 4 || !got.ExpiresAt.Equal(pair.Object.ValidUntil) {
		t.Fatalf("caught-up object decided under parent %d expiring %s", got.ParentRevision, got.ExpiresAt)
	}
	if _, ok := got.Clusters["us"]; !ok || len(got.Clusters) != 2 {
		t.Fatalf("held revision census: %v", got.Clusters)
	}
}
