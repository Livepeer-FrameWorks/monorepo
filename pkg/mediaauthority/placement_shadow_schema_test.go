package mediaauthority

import (
	"testing"

	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestPlacementShadowComparableRequiresCoherentSchema2Pair(t *testing.T) {
	for _, tenantSchema := range []uint32{1, 2} {
		for _, objectSchema := range []uint32{1, 2} {
			for _, shape := range []string{"complete", "tenant-policy-missing", "object-policy-missing", "parent-mismatch"} {
				tenant := &mediapb.TenantAuthority{SchemaVersion: tenantSchema, MediaPlacement: &placementpb.PolicySet{Revision: 4}}
				object := &mediapb.MediaObjectAuthority{SchemaVersion: objectSchema, MediaPlacement: &placementpb.PolicySet{Revision: 2}, PlacementTenantRevision: 4}
				switch shape {
				case "tenant-policy-missing":
					tenant.MediaPlacement = nil
				case "object-policy-missing":
					object.MediaPlacement = nil
				case "parent-mismatch":
					object.PlacementTenantRevision = 3
				}
				want := tenantSchema == 2 && objectSchema == 2 && shape == "complete"
				if PlacementShadowComparable(tenant, object) != want {
					t.Fatalf("schema %d/%d, shape %s: want comparable=%t", tenantSchema, objectSchema, shape, want)
				}
				if LegacyShadowComparable(tenant, object) {
					t.Fatalf("schema %d/%d, shape %s: legacy comparison accepted placement fields", tenantSchema, objectSchema, shape)
				}
			}
		}
	}
	if PlacementShadowComparable(nil, nil) {
		t.Fatal("missing authorities became comparable")
	}
}
