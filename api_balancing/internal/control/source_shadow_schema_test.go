package control

import (
	"context"
	"testing"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

// The mismatch cases below all stop at the schema comparison, so they would
// still pass if that comparison simply refused everything. This pins the other
// side: a matched pair IS comparable, which is what makes the refusals
// attributable to the schema difference rather than to a blanket false.
func TestPullSourceShadowSchemaComparisonDiscriminates(t *testing.T) {
	authority := func(schema uint32) (*mediapb.TenantAuthority, *mediapb.MediaObjectAuthority) {
		return &mediapb.TenantAuthority{SchemaVersion: schema, TenantId: "tenant"},
			&mediapb.MediaObjectAuthority{SchemaVersion: schema, TenantId: "tenant"}
	}
	tenant, object := authority(1)
	if !localauthority.ShadowComparable(tenant, object) {
		t.Fatal("a matched schema pair was reported as not comparable")
	}
	tenant2, _ := authority(2)
	if localauthority.ShadowComparable(tenant2, object) {
		t.Fatal("a mismatched schema pair was reported as comparable")
	}
}

func TestPullSourceShadowCannotPromotePlacementSchema(t *testing.T) {
	for _, tenantSchema := range []uint32{1, 2} {
		for _, objectSchema := range []uint32{1, 2} {
			if tenantSchema == 1 && objectSchema == 1 {
				// Promotion would reach the store, which has no database here.
				// TestPullSourceShadowSchemaComparisonDiscriminates covers this
				// pair at the comparison itself.
				continue
			}
			tenant := &mediapb.TenantAuthority{SchemaVersion: tenantSchema, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW}
			object := &mediapb.MediaObjectAuthority{SchemaVersion: objectSchema, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "pull"}}}
			connected := &commodorepb.ResolvePullSourceByInternalNameResponse{Found: true, TenantId: "tenant", StreamId: "stream", SourceUri: "https://source.example/live", Enabled: true}
			stream := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", BillingModel: "postpaid", TenantResourceLimits: sharedauthority.EffectiveResourceLimits(tenant, "")}
			if !sameLocalSourceTenant(tenant, "", stream) {
				t.Fatal("fixture does not match the legacy tenant comparison")
			}
			snapshot := localauthority.SourceSnapshot{Tenant: localauthority.TenantSnapshot{Authority: tenant}, Object: localauthority.MediaObjectSnapshot{Authority: object},
				Secret: &mediapb.LiveStreamSecret{SourceUri: connected.SourceUri, SourceEnabled: true}}
			// No database is configured: a schema rejection must precede promotion I/O.
			got, err := PromoteLocalPullSourceIfMatching(context.Background(), &localauthority.Store{}, connected, stream, snapshot)
			if err != nil || got != PullSourcePromotionMismatch {
				t.Fatalf("schema %d/%d: promotion=%s, %v", tenantSchema, objectSchema, got, err)
			}
		}
	}
}
