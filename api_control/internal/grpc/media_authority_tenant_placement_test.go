package grpc

import (
	"context"
	"testing"

	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestTenantPlacementRevocationIsIndependentAndDetached(t *testing.T) {
	tenant, _ := commercialAuthorityFixture()
	server := &CommodoreServer{}
	for _, lifecycle := range []mediapb.AuthorityLifecycle{mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE, mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE} {
		payload := &mediapb.TenantAuthority{SchemaVersion: 1, TenantId: tenant.TenantId, Lifecycle: lifecycle}
		if err := server.inheritTenantPlacement(context.Background(), payload, nil, tenant); err != nil {
			t.Fatal(err)
		}
		if payload.SchemaVersion != 2 || !proto.Equal(payload.MediaPlacement, tenant.MediaPlacement) {
			t.Fatal("revocation lost placement fence")
		}
		payload.MediaPlacement.Revision++
		if tenant.MediaPlacement.Revision != 3 {
			t.Fatal("revocation aliases previous policy")
		}
	}
	payload := proto.CloneOf(tenant)
	payload.Lifecycle = mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
	before := proto.CloneOf(payload)
	if err := server.inheritTenantPlacement(context.Background(), payload, nil, tenant); err == nil || !proto.Equal(payload, before) {
		t.Fatal("revocation retained positive grants or mutated a rejected payload")
	}
}

func TestTenantPlacementHistoryRejectsAmbiguousSchemaAndPolicy(t *testing.T) {
	for _, scenario := range []string{"unknown schema", "foreign tenant", "missing policy", "legacy policy", "invalid policy"} {
		t.Run(scenario, func(t *testing.T) {
			tenant, _ := commercialAuthorityFixture()
			wantID := tenant.TenantId
			switch scenario {
			case "unknown schema":
				tenant.SchemaVersion = 99
			case "foreign tenant":
				tenant.TenantId = "foreign"
			case "missing policy":
				tenant.MediaPlacement = nil
			case "legacy policy":
				tenant.SchemaVersion = 1
			case "invalid policy":
				tenant.MediaPlacement = &pb.PolicySet{Revision: 0, Serve: &pb.Rules{SchemaVersion: 1}}
			}
			encoded, err := proto.Marshal(tenant)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeTenantPlacementHistory(encoded, wantID); err == nil {
				t.Fatal("ambiguous tenant history accepted")
			}
		})
	}
}

func TestTenantPlacementSourceRevisionsAreCanonicalWithoutReorderingInput(t *testing.T) {
	tenant, _ := commercialAuthorityFixture()
	sources := []*mediapb.AuthoritySourceRevision{{Service: "purser", Revision: "billing"}, {Service: "quartermaster", Revision: "membership"}}
	got, err := tenantPlacementSourceRevisions(tenant, sources)
	if err != nil || len(got) != 3 || got[0].Service != "commodore" || got[1].Service != "purser" || got[2].Service != "quartermaster" || sources[0].Service != "purser" {
		t.Fatalf("noncanonical or mutating policy provenance: %v %v", got, err)
	}
	want, err := hashProtoMessages(tenant.MediaPlacement)
	if err != nil || got[0].Revision != want {
		t.Fatalf("policy provenance does not match signed intent: %v", err)
	}
	tenant.SchemaVersion, tenant.MediaPlacement = 1, nil
	got, err = tenantPlacementSourceRevisions(tenant, sources)
	if err != nil || len(got) != 2 {
		t.Fatalf("legacy publication gained policy provenance: %v", err)
	}
}
