package mediaauthority

import (
	"testing"

	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestLegacyShadowComparisonCannotCertifyPlacementReadiness(t *testing.T) {
	for _, tenantSchema := range []uint32{0, 1, 2, 99} {
		for _, objectSchema := range []uint32{0, 1, 2, 99} {
			for _, policy := range []string{"absent", "tenant-default", "object-default", "parent"} {
				tenant := &mediapb.TenantAuthority{SchemaVersion: tenantSchema}
				object := &mediapb.MediaObjectAuthority{SchemaVersion: objectSchema}
				switch policy {
				case "tenant-default":
					tenant.MediaPlacement = &placementpb.PolicySet{}
				case "object-default":
					object.MediaPlacement = &placementpb.PolicySet{}
				case "parent":
					object.PlacementTenantRevision = 1
				}
				want := tenantSchema == 1 && objectSchema == 1 && policy == "absent"
				if LegacyShadowComparable(tenant, object) != want {
					t.Fatalf("schema %d/%d, policy %s: want comparable=%t", tenantSchema, objectSchema, policy, want)
				}
			}
		}
	}
	if LegacyShadowComparable(nil, nil) {
		t.Fatal("missing authorities became comparable")
	}
}
