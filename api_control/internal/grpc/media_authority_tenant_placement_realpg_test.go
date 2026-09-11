//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
)

type tenantPlacementOwnerFixture struct {
	placementManagementOwnerFixture
	entitlement                  *quartermasterpb.GetTenantEntitlementResponse
	inactive, deleted, suspended bool
}

func (f *tenantPlacementOwnerFixture) GetTenant(_ context.Context, id string) (*quartermasterpb.GetTenantResponse, error) {
	if f.deleted {
		return &quartermasterpb.GetTenantResponse{Error: "tenant not found"}, nil
	}
	return &quartermasterpb.GetTenantResponse{Tenant: &quartermasterpb.Tenant{Id: id, IsActive: !f.inactive}}, nil
}

func (f *tenantPlacementOwnerFixture) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	return proto.CloneOf(f.entitlement), nil
}

func (f *tenantPlacementOwnerFixture) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	return &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid", IsSuspended: f.suspended}, nil
}

func TestMediaPlacementTenantRefresh_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tenant, _ := commercialAuthorityFixture()
	tenant.MediaPlacement.Revision = 1
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer := &clusterpb.TenantClusterPeer{ClusterId: "official", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, ControlCellId: "cell-a", RegionId: "us-east", MediaConsent: &pb.CapacityConsent{Revision: 7, AllowIngest: true, AllowServe: true, AllowExternalSource: true}}
	owner := &tenantPlacementOwnerFixture{entitlement: &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"official"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{peer}}}
	server := &CommodoreServer{db: db, authorityTenantSource: owner, authorityBillingSource: owner, mediaAuthorityKeyID: "tenant-placement", mediaAuthorityPrivateKey: private}
	store := placementpolicy.NewStore(db)
	command := placementpolicy.ApplyInput{Scope: placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "tenant", ID: tenant.TenantId}, ActorID: "tenant-policy-test", IdempotencyKey: "first", ReviewDigest: strings.Repeat("a", 64), Policy: tenant.MediaPlacement}
	if _, err := store.Apply(ctx, command, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	q := commodoredb.New(db)
	read := func(wantVersion int64, wantSchema uint32) *mediapb.TenantAuthority {
		t.Helper()
		row, err := q.LockCurrentTenantMediaAuthority(ctx, tenant.TenantId)
		if err != nil {
			t.Fatal(err)
		}
		payload := &mediapb.TenantAuthority{}
		if err := proto.Unmarshal(row.Payload, payload); err != nil {
			t.Fatal(err)
		}
		var version int64
		if err := db.QueryRowContext(ctx, "SELECT authority_version FROM commodore.media_authority_current WHERE authority_kind='tenant' AND authority_id=$1", tenant.TenantId).Scan(&version); err != nil || version != wantVersion || payload.SchemaVersion != wantSchema {
			t.Fatalf("unexpected tenant publication: version=%d schema=%d err=%v", version, payload.SchemaVersion, err)
		}
		var signedBytes []byte
		if err := db.QueryRowContext(ctx, "SELECT signed_envelope FROM commodore.media_authority_deliveries WHERE authority_kind='tenant' AND authority_id=$1 AND authority_version=$2 AND cell_id='cell-a'", tenant.TenantId, version).Scan(&signedBytes); err != nil {
			t.Fatal(err)
		}
		signed := &mediapb.SignedAuthorityEnvelope{}
		if err := proto.Unmarshal(signedBytes, signed); err != nil {
			t.Fatal(err)
		}
		verified, err := sharedauthority.Verify(signed, sharedauthority.TrustSet{"tenant-placement": public}, "cell-a", time.Now())
		if err != nil || !proto.Equal(verified.Tenant, payload) {
			t.Fatalf("delivery lost signed tenant placement: %v", err)
		}
		if wantSchema == 2 {
			want, err := hashProtoMessages(payload.MediaPlacement)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, revision := range signed.Envelope.SourceRevisions {
				if revision.Service == "commodore" && revision.Revision == want {
					found = true
				}
			}
			if !found {
				t.Fatal("signed tenant lacks the policy owner's revision")
			}
		}
		return payload
	}
	// Requested intent alone does not authorize the schema cutover.
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	legacy := read(1, 1)
	if legacy.MediaPlacement != nil || legacy.EffectiveClusterGrants[0].MediaConsent != nil {
		t.Fatal("first refresh activated unapproved placement")
	}
	if err := seedCommercialTenantVersion(ctx, db, tenant, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := q.AllocateMediaAuthorityVersion(ctx, commodoredb.AllocateMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId}); err != nil {
		t.Fatal(err)
	}
	// Seeded schema-2 history models an activated tenant; publication still
	// requires the target cell's own enforcement attestation.
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err == nil || !strings.Contains(err.Error(), "placement enforcement capability") {
		t.Fatalf("schema-2 publication reached an unattested cell: %v", err)
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	compiled := read(3, 2)
	if compiled.MediaPlacement.Revision != 1 || compiled.EffectiveClusterGrants[0].RegionId != "us-east" || !proto.Equal(compiled.EffectiveClusterGrants[0].MediaConsent, peer.MediaConsent) || compiled.EffectiveClusterGrants[0].CommercialFacts != nil {
		t.Fatal("tenant refresh lost policy, owner consent or region, or invented commercial facts")
	}
	wantDigest, err := placement.CommercialEntitlementDigest(tenant.TenantId, owner.entitlement.EffectiveAccess)
	if err != nil {
		t.Fatal(err)
	}
	gotDigest, err := sharedauthority.PlacementCommercialEntitlementDigest(compiled)
	if err != nil || gotDigest != wantDigest {
		t.Fatalf("signed entitlement differs from owner: %v", err)
	}
	peer.MediaConsent.Revision++
	peer.MediaConsent.AllowServe = false
	command.ExpectedRevision, command.IdempotencyKey, command.Policy = 1, "second", &pb.PolicySet{Revision: 2}
	if _, err := store.Apply(ctx, command, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	compiled = read(4, 2)
	if compiled.MediaPlacement.Revision != 2 || compiled.EffectiveClusterGrants[0].MediaConsent.Revision != 8 || compiled.EffectiveClusterGrants[0].MediaConsent.AllowServe {
		t.Fatal("renewal retained superseded policy or consent")
	}
	for _, scenario := range []string{"schema", "revision", "same revision mutation"} {
		candidate := proto.CloneOf(compiled)
		switch scenario {
		case "schema":
			candidate = legacy
		case "revision":
			candidate.MediaPlacement.Revision--
		case "same revision mutation":
			candidate.MediaPlacement.Serve = &pb.Rules{SchemaVersion: 1}
		}
		if err := server.persistTenantAuthority(ctx, candidate, []string{"cell-a"}, nil, time.Now(), time.Now().Add(time.Hour)); err == nil {
			t.Fatalf("accepted %s rollback", scenario)
		}
		read(4, 2)
	}
	for _, scenario := range []string{"missing consent", "missing membership", "duplicate membership", "duplicate peer", "unknown owner evidence", "invalid owner"} {
		saved := owner.entitlement
		owner.entitlement = proto.CloneOf(saved)
		switch scenario {
		case "missing consent":
			owner.entitlement.EffectiveAccess[0].MediaConsent = nil
		case "missing membership":
			owner.entitlement.AllowedClusterIds = nil
		case "duplicate membership":
			owner.entitlement.AllowedClusterIds = append(owner.entitlement.AllowedClusterIds, "official")
		case "duplicate peer":
			owner.entitlement.EffectiveAccess = append(owner.entitlement.EffectiveAccess, proto.CloneOf(peer))
		case "unknown owner evidence":
			owner.entitlement.EffectiveAccess[0].MediaConsent.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		case "invalid owner":
			owner.entitlement.EffectiveAccess[0].OwnerTenantId = "not-a-tenant"
		}
		if err := server.compileTenantAuthority(ctx, tenant.TenantId); err == nil {
			t.Fatalf("invalid owner response became authority: %s", scenario)
		}
		read(4, 2)
		owner.entitlement = saved
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_placement_policies SET policy_payload=$2 WHERE tenant_id=$1 AND scope_kind='tenant' AND scope_id=$1", tenant.TenantId, []byte{0xff}); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err == nil {
		t.Fatal("corrupt requested policy was ignored")
	}
	for index, scenario := range []string{"suspended", "inactive", "deleted"} {
		owner.suspended, owner.inactive, owner.deleted = scenario == "suspended", scenario == "inactive", scenario == "deleted"
		if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
			t.Fatalf("revocation depends on corrupt policy: %s: %v", scenario, err)
		}
		denied := read(int64(5+index), 2)
		if denied.MediaPlacement.Revision != 2 || len(denied.EffectiveClusterGrants) != 0 || denied.BillingDecision == mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
			t.Fatalf("revocation lost fence or retained positive authority: %s", scenario)
		}
	}
}
