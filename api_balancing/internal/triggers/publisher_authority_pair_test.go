package triggers

import (
	"crypto/ed25519"
	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestReturnedPublisherPairCannotPersistAnUnrelatedAuthority(t *testing.T) {
	processor, mock, cleanup, _, _ := localAuthorityFixture(t)
	defer cleanup()
	encode := func(kind mediapb.AuthorityKind, id string) []byte {
		t.Helper()
		wire, err := proto.Marshal(&mediapb.SignedAuthorityEnvelope{Envelope: &mediapb.AuthorityEnvelope{Kind: kind, AuthorityId: id}})
		if err != nil {
			t.Fatal(err)
		}
		return wire
	}
	pair := [][]byte{encode(mediapb.AuthorityKind_AUTHORITY_KIND_TENANT, "other-tenant"), encode(mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, "live_stream:stream")}
	if err := processor.applyPublisherAuthorityPair(t.Context(), pair, "tenant", "stream"); err == nil {
		t.Fatal("unrelated authority pair accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherHardPolicyPreflightRequiresProofAndExactNode(t *testing.T) {
	now := time.Now()
	local := localIngestAuthority{
		tenant: localauthority.TenantSnapshot{Version: 1, IngestReady: true, ValidUntil: now.Add(time.Minute), Authority: &mediapb.TenantAuthority{
			SchemaVersion: 3, TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, MediaPlacement: &placementpb.PolicySet{Revision: 1},
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "private", ControlCellId: "cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant", SubscriptionStatus: "active", MediaConsent: &placementpb.CapacityConsent{AllowIngest: true}}},
		}},
		object: localauthority.MediaObjectSnapshot{AuthorityID: "live_stream:stream", Version: 1, IngestReady: true, ValidUntil: now.Add(time.Minute), Authority: &mediapb.MediaObjectAuthority{
			SchemaVersion: 3, TenantId: "tenant", InternalName: "internal", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, PlacementTenantRevision: 1,
			MediaPlacement: &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{NodeIds: []string{"allowed"}}}}}}},
			Object:         &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}},
		}},
	}
	if _, err := balancer.CompilePlacementAuthority(localauthority.PlacementPair{Tenant: local.tenant, Object: local.object}, placement.Ingest, now); err != nil {
		t.Fatalf("preflight fixture is not valid authority: %v", err)
	}
	if publisherHardPolicyDenied(local, "private", "allowed", now) {
		t.Fatal("permitted node was denied before its final admission")
	}
	if !publisherHardPolicyDenied(local, "private", "other", now) || !publisherHardPolicyDenied(local, "public", "allowed", now) {
		t.Fatal("proven node/cluster denial was not detected before taking a lease")
	}
	local.object.IngestReady = false
	if publisherHardPolicyDenied(local, "private", "other", now) {
		t.Fatal("unready authority was misreported as a definitive placement refusal")
	}
}

func TestReturnedPublisherPairUsesStoreSignatureAndAudienceVerification(t *testing.T) {
	for _, scenario := range []string{"bad-signature", "wrong-cell"} {
		t.Run(scenario, func(t *testing.T) {
			processor, mock, cleanup, tenantBytes, objectBytes := localAuthorityFixture(t)
			defer cleanup()
			tenant, object := &mediapb.TenantAuthority{}, &mediapb.MediaObjectAuthority{}
			if err := proto.Unmarshal(tenantBytes, tenant); err != nil {
				t.Fatal(err)
			}
			if err := proto.Unmarshal(objectBytes, object); err != nil {
				t.Fatal(err)
			}
			key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
			now := time.Now().UTC()
			encode := func(kind mediapb.AuthorityKind, id string, payload proto.Message) []byte {
				t.Helper()
				cell := "cell-a"
				if scenario == "wrong-cell" {
					cell = "other-cell"
				}
				envelope, err := sharedauthority.NewEnvelope(kind, id, 2, now.Add(-time.Second), now.Add(time.Minute), now.Add(2*time.Minute), "key", cell, payload, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "pair-test"}})
				if err != nil {
					t.Fatal(err)
				}
				signed, err := sharedauthority.Sign(envelope, key)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "bad-signature" {
					signed.Signature[0] ^= 1
				}
				encoded, err := proto.Marshal(signed)
				if err != nil {
					t.Fatal(err)
				}
				return encoded
			}
			pair := [][]byte{encode(mediapb.AuthorityKind_AUTHORITY_KIND_TENANT, tenant.GetTenantId(), tenant), encode(mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId()), object)}
			mock.ExpectExec("INSERT INTO foghorn.media_authority_apply_audit").WillReturnResult(sqlmock.NewResult(0, 1))
			err := processor.applyPublisherAuthorityPair(t.Context(), pair, tenant.GetTenantId(), object.GetLiveStream().GetStreamId())
			want := "signature"
			if scenario == "wrong-cell" {
				want = "audience"
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), want) {
				t.Fatalf("Store did not reject %s: %v", scenario, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
