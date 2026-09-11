package mediaauthority

import (
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func placementTenant() *mediaauthoritypb.TenantAuthority {
	tenant := fixtureTenant()
	tenant.SchemaVersion = PlacementSchemaVersion
	tenant.MediaPlacement = &placementpb.PolicySet{Revision: 7, Serve: &placementpb.Rules{
		SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}}},
	}}
	tenant.EffectiveClusterGrants[0].MediaConsent = &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}
	return tenant
}

func placementObject() *mediaauthoritypb.MediaObjectAuthority {
	return &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: PlacementSchemaVersion, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId: "tenant-1", InternalName: "internal-1", PlaybackId: "playback-1", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object:         &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream-1", IngestMode: "push"}},
		MediaPlacement: &placementpb.PolicySet{}, PlacementTenantRevision: 7,
	}
}

func TestPlacementAuthorityRoundTripAndRevisionFence(t *testing.T) {
	tenant, object := placementTenant(), placementObject()
	tenant.EffectiveClusterGrants[0].CommercialFacts = &placementpb.CommercialFacts{Charging: placementpb.Charging_CHARGING_RATED, Revision: "classification-1", ExpiresAt: timestamppb.New(fixtureNow.Add(48 * time.Hour))}
	for _, payload := range []proto.Message{tenant, object} {
		signed, trust := fixtureSigned(t, payload)
		if signed.GetEnvelope().GetSchemaVersion() != PlacementSchemaVersion {
			t.Fatal("placement signed with legacy envelope version")
		}
		verified, err := Verify(signed, trust, "cell-a", fixtureNow.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if verified.Tenant != nil && !proto.Equal(verified.Tenant, tenant) {
			t.Fatal("signed tenant round trip lost charging classification")
		}
	}
	p, err := EffectivePlacement(tenant, object, placement.Serve)
	if err != nil || len(p.Layers) != 1 || len(p.Layers[0].Deny) != 1 {
		t.Fatalf("effective policy: %+v %v", p, err)
	}
	object.PlacementTenantRevision++
	if _, err := EffectivePlacement(tenant, object, placement.Serve); !errors.Is(err, ErrMalformed) {
		t.Fatalf("mixed parent revisions accepted: %v", err)
	}
	object.PlacementTenantRevision--
	object.TenantId = "another-tenant"
	if _, err := EffectivePlacement(tenant, object, placement.Serve); !errors.Is(err, ErrMalformed) {
		t.Fatalf("cross-tenant overlay accepted: %v", err)
	}
	object = placementObject()
	object.SchemaVersion = SchemaVersion
	if _, err := EffectivePlacement(tenant, object, placement.Serve); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("mixed schema delivery accepted: %v", err)
	}
}

func TestPlacementAuthorityCannotDowngradeOrOmitConsent(t *testing.T) {
	for name, mutate := range map[string]func(*mediaauthoritypb.TenantAuthority){
		"legacy payload":        func(p *mediaauthoritypb.TenantAuthority) { p.SchemaVersion = SchemaVersion },
		"missing placement":     func(p *mediaauthoritypb.TenantAuthority) { p.MediaPlacement = nil },
		"missing owner consent": func(p *mediaauthoritypb.TenantAuthority) { p.EffectiveClusterGrants[0].MediaConsent = nil },
		"legacy grant facts":    func(p *mediaauthoritypb.TenantAuthority) { p.SchemaVersion = SchemaVersion; p.MediaPlacement = nil },
		"expired commercial facts": func(p *mediaauthoritypb.TenantAuthority) {
			p.EffectiveClusterGrants[0].CommercialFacts = &placementpb.CommercialFacts{Charging: placementpb.Charging_CHARGING_RATED, Revision: "billing:1", ExpiresAt: timestamppb.New(fixtureNow.Add(time.Minute))}
		},
		"unknown charging": func(p *mediaauthoritypb.TenantAuthority) {
			p.EffectiveClusterGrants[0].CommercialFacts = &placementpb.CommercialFacts{Charging: 99, Revision: "billing:1", ExpiresAt: timestamppb.New(fixtureNow.Add(48 * time.Hour))}
		},
		"false free price": func(p *mediaauthoritypb.TenantAuthority) {
			p.EffectiveClusterGrants[0].CommercialFacts = &placementpb.CommercialFacts{Charging: placementpb.Charging_CHARGING_PERMANENTLY_FREE, Revision: "billing:1", ExpiresAt: timestamppb.New(fixtureNow.Add(48 * time.Hour)), ServePrice: &placementpb.Price{AmountMicros: 1, Currency: "EUR", Unit: "minute"}}
		},
		"duplicate price collection": func(p *mediaauthoritypb.TenantAuthority) {
			quote := &placementpb.Price{Currency: "EUR", Unit: "serve:minutes=1;gib=2"}
			p.EffectiveClusterGrants[0].CommercialFacts = &placementpb.CommercialFacts{Charging: placementpb.Charging_CHARGING_RATED, Revision: "quotes-1", ExpiresAt: timestamppb.New(fixtureNow.Add(48 * time.Hour)), ServePrice: quote, ServePrices: []*placementpb.Price{proto.CloneOf(quote)}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			payload := placementTenant()
			mutate(payload)
			_, err := NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, "tenant-1", 1, fixtureNow, fixtureNow.Add(time.Minute), fixtureNow.Add(time.Hour), "key", "cell-a", payload, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}})
			if err == nil {
				t.Fatal("invalid authority accepted")
			}
		})
	}
}

func TestPlacementEnvelopePayloadSchemaMustMatch(t *testing.T) {
	envelope := fixtureEnvelope(t, placementTenant())
	envelope.SchemaVersion = SchemaVersion
	_, privateKey := fixtureKeys()
	if _, err := Sign(envelope, privateKey); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("placement payload signed inside legacy envelope: %v", err)
	}
	object := placementObject()
	object.SchemaVersion = SchemaVersion
	_, err := NewEnvelope(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, LiveStreamAuthorityID("stream-1"), 1, fixtureNow, fixtureNow.Add(time.Minute), fixtureNow.Add(time.Hour), "key", "cell-a", object, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "1"}})
	if !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("placement object downgraded: %v", err)
	}
}
