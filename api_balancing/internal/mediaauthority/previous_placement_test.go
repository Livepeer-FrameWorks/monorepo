package mediaauthority

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func heldTenantRow(t *testing.T, schema uint32, placementSet *placementpb.PolicySet, validUntil time.Time) foghorndb.GetMediaAuthorityForUpdateRow {
	t.Helper()
	payload, err := proto.Marshal(&mediaauthoritypb.TenantAuthority{SchemaVersion: schema, TenantId: "tenant", MediaPlacement: placementSet})
	if err != nil {
		t.Fatal(err)
	}
	return foghorndb.GetMediaAuthorityForUpdateRow{AuthorityVersion: 6, Payload: payload, ValidUntil: validUntil}
}

func verifiedTenant(schema uint32, revision uint64) *sharedauthority.Verified {
	return &sharedauthority.Verified{Tenant: &mediaauthoritypb.TenantAuthority{SchemaVersion: schema, TenantId: "tenant", MediaPlacement: &placementpb.PolicySet{Revision: revision}}}
}

func TestPreviousTenantPlacementRecordsOnlyARaisedRevision(t *testing.T) {
	now := storeFixtureNow
	replaced := &placementpb.PolicySet{Revision: 3, Serve: &placementpb.Rules{SchemaVersion: 1}}
	for _, tc := range []struct {
		name       string
		held       foghorndb.GetMediaAuthorityForUpdateRow
		hasCurrent bool
		verified   *sharedauthority.Verified
		want       string
	}{
		{"raised revision records the replaced set", heldTenantRow(t, 2, replaced, now.Add(time.Hour)), true, verifiedTenant(2, 4), previousPlacementRecord},
		{"same revision keeps what was recorded", heldTenantRow(t, 2, replaced, now.Add(time.Hour)), true, verifiedTenant(2, 3), previousPlacementKeep},
		{"first tenant authority records nothing", foghorndb.GetMediaAuthorityForUpdateRow{}, false, verifiedTenant(2, 4), previousPlacementClear},
		{"schema change records nothing", heldTenantRow(t, 2, replaced, now.Add(time.Hour)), true, verifiedTenant(3, 4), previousPlacementClear},
		{"held authority past its validity records nothing", heldTenantRow(t, 2, replaced, now), true, verifiedTenant(2, 4), previousPlacementClear},
		{"legacy schema records nothing", heldTenantRow(t, 1, nil, now.Add(time.Hour)), true, verifiedTenant(1, 0), previousPlacementClear},
		{"object authority records nothing", heldTenantRow(t, 2, replaced, now.Add(time.Hour)), true, &sharedauthority.Verified{MediaObject: &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 2}}, previousPlacementClear},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := previousTenantPlacement(tc.held, tc.hasCurrent, tc.verified, now)
			if err != nil {
				t.Fatal(err)
			}
			if got.mode != tc.want {
				t.Fatalf("mode %q, want %q", got.mode, tc.want)
			}
			if tc.want != previousPlacementRecord {
				if got.policy != nil {
					t.Fatalf("%s carries a policy", got.mode)
				}
				return
			}
			recorded := &placementpb.PolicySet{}
			if err := proto.Unmarshal(got.policy, recorded); err != nil || !proto.Equal(recorded, replaced) {
				t.Fatalf("recorded %v (%v), want the replaced set %v", recorded, err, replaced)
			}
		})
	}
}
