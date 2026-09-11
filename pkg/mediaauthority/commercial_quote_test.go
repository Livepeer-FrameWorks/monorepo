package mediaauthority

import (
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func objectCommercialFixture(t *testing.T) (*mediapb.TenantAuthority, *mediapb.MediaObjectAuthority) {
	t.Helper()
	tenant, object := placementTenant(), placementObject()
	tenant.TenantId = "81000000-0000-4000-8000-000000000001"
	object.TenantId = tenant.TenantId
	object.MediaPlacement.Revision = 2
	object.MediaPlacement.Serve = &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{
		{Id: "first", Order: pb.Order_ORDER_PRICE, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=2"},
		{Id: "same-basis", Order: pb.Order_ORDER_PRICE, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=2"},
		{Id: "different-basis", Order: pb.Order_ORDER_PRICE, PriceCurrency: "EUR", PriceUnit: "serve:minutes=60;gib=2"},
	}}}
	request, err := PlacementCommercialRequest(tenant, object, placement.Serve)
	if err != nil {
		t.Fatal(err)
	}
	_, digest, err := placement.CanonicalCommercialQuoteRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	quote := &pb.CommercialQuoteResponse{Scope: request, RequestDigest: digest, ObservedAt: timestamppb.New(fixtureNow), ExpiresAt: timestamppb.New(fixtureNow.Add(30 * time.Second)), Usage: &pb.QuoteUsageEvidence{Status: pb.QuoteUsageStatus_QUOTE_USAGE_STATUS_COVERED_THROUGH_CUTOFF, PeriodStart: timestamppb.New(fixtureNow.Add(-time.Hour)), PeriodEnd: timestamppb.New(fixtureNow.Add(time.Hour)), Through: timestamppb.New(fixtureNow.Add(-5 * time.Minute))}}
	quote.EntitlementDigest, err = PlacementCommercialEntitlementDigest(tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range request.ClusterIds {
		cluster := &pb.ClusterCommercialQuote{ClusterId: id, Facts: &pb.CommercialFacts{Charging: pb.Charging_CHARGING_RATED, Revision: strings.Repeat("b", 64), ExpiresAt: proto.CloneOf(quote.ExpiresAt)}}
		for _, basis := range request.Bases {
			cluster.Facts.ServePrices = append(cluster.Facts.ServePrices, &pb.Price{AmountMicros: 9007199254740993, Currency: basis.Currency, Unit: basis.Unit})
			cluster.RecordedUsageBases = append(cluster.RecordedUsageBases, proto.CloneOf(basis))
		}
		quote.Clusters = append(quote.Clusters, cluster)
	}
	object.CommercialQuotes = []*pb.CommercialQuoteResponse{quote}
	return tenant, object
}

func objectCommercialEnvelope(object *mediapb.MediaObjectAuthority, until time.Time) (*mediapb.AuthorityEnvelope, error) {
	return NewEnvelope(mediapb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, placementCommercialObjectID(object), 7, fixtureNow, fixtureNow.Add(5*time.Second), until, "key-2026-08", "cell-a", object, []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "policy-2"}, {Service: "purser", Revision: "quote-1"}})
}

func TestObjectCommercialQuoteSignedRoundTripAndDetachedJoin(t *testing.T) {
	tenant, object := objectCommercialFixture(t)
	if len(object.CommercialQuotes[0].Scope.Bases) != 2 || len(object.CommercialQuotes[0].Scope.ClusterIds) != len(tenant.EffectiveClusterGrants) {
		t.Fatal("comparison groups were not deduplicated or constraints narrowed census")
	}
	envelope, err := objectCommercialEnvelope(object, fixtureNow.Add(25*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	public, private := fixtureKeys()
	signed, err := Sign(envelope, private)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(signed, TrustSet{"key-2026-08": public}, "cell-a", fixtureNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(verified.MediaObject, object) {
		t.Fatal("signed round trip lost quote/cutoff evidence")
	}
	quote, err := PlacementCommercialQuote(tenant, verified.MediaObject, placement.Serve, fixtureNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if quote.Clusters[0].Facts.ServePrices[0].AmountMicros != 9007199254740993 || !proto.Equal(quote.Usage, object.CommercialQuotes[0].Usage) {
		t.Fatal("join lost exact price or usage evidence")
	}
	quote.Usage.Through = timestamppb.New(fixtureNow)
	quote.Clusters[0].Facts.ServePrices[0].AmountMicros = 1
	if !proto.Equal(verified.MediaObject, object) {
		t.Fatal("quote projection mutated signed authority")
	}
	if quote, err = PlacementCommercialQuote(tenant, object, placement.Ingest, fixtureNow); err != nil || quote != nil {
		t.Fatalf("serve quote reused for ingest: %+v %v", quote, err)
	}
}

func TestObjectCommercialQuoteEnvelopeRejectsRebindingAndLeaseExtension(t *testing.T) {
	for name, mutate := range map[string]func(*mediapb.MediaObjectAuthority){
		"wrong tenant": func(o *mediapb.MediaObjectAuthority) {
			o.CommercialQuotes[0].Scope.TenantId = "81000000-0000-4000-8000-000000000002"
		},
		"wrong object":          func(o *mediapb.MediaObjectAuthority) { o.CommercialQuotes[0].Scope.ObjectId = "live_stream:other" },
		"wrong policy revision": func(o *mediapb.MediaObjectAuthority) { o.CommercialQuotes[0].Scope.PolicyRevision++ },
		"wrong parent":          func(o *mediapb.MediaObjectAuthority) { o.CommercialQuotes[0].Scope.ParentRevision++ },
		"duplicate verb": func(o *mediapb.MediaObjectAuthority) {
			o.CommercialQuotes = append(o.CommercialQuotes, proto.CloneOf(o.CommercialQuotes[0]))
		},
		"too many":  func(o *mediapb.MediaObjectAuthority) { o.CommercialQuotes = append(o.CommercialQuotes, nil, nil) },
		"nil quote": func(o *mediapb.MediaObjectAuthority) { o.CommercialQuotes[0] = nil },
		"expired":   func(o *mediapb.MediaObjectAuthority) { o.CommercialQuotes[0].ExpiresAt = timestamppb.New(fixtureNow) },
		"unknown nested evidence": func(o *mediapb.MediaObjectAuthority) {
			o.CommercialQuotes[0].Usage.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
		},
		"legacy": func(o *mediapb.MediaObjectAuthority) {
			o.SchemaVersion = SchemaVersion
			o.MediaPlacement = nil
			o.PlacementTenantRevision = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, object := objectCommercialFixture(t)
			mutate(object)
			if _, err := objectCommercialEnvelope(object, fixtureNow.Add(25*time.Second)); err == nil {
				t.Fatal("invalid quote signed")
			}
		})
	}
	_, object := objectCommercialFixture(t)
	if _, err := objectCommercialEnvelope(object, fixtureNow.Add(31*time.Second)); err == nil {
		t.Fatal("authority extended commercial lease")
	}
}

func TestObjectCommercialQuoteJoinRequiresExactEffectivePolicyAndCensus(t *testing.T) {
	for name, mutate := range map[string]func(*mediapb.TenantAuthority, *mediapb.MediaObjectAuthority){
		"owner transfer": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) {
			t.EffectiveClusterGrants[0].OwnerTenantId = "81000000-0000-4000-8000-000000000002"
		},
		"consent changed": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) {
			t.EffectiveClusterGrants[0].MediaConsent.AllowServe = false
		},
		"access expiry changed": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) {
			t.EffectiveClusterGrants[0].ExpiresAt = timestamppb.New(fixtureNow.Add(time.Hour))
		},
		"parent constraints": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) {
			t.MediaPlacement.Serve.Constraints = nil
		},
		"object ratio": func(_ *mediapb.TenantAuthority, o *mediapb.MediaObjectAuthority) {
			o.MediaPlacement.Serve.Preferences.Groups[0].PriceUnit = "serve:minutes=2;gib=2"
		},
		"new grant": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) {
			g := proto.CloneOf(t.EffectiveClusterGrants[0])
			g.ClusterId = "new-cluster"
			t.EffectiveClusterGrants = append(t.EffectiveClusterGrants, g)
		},
		"removed grant": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) { t.EffectiveClusterGrants = nil },
		"duplicate grant": func(t *mediapb.TenantAuthority, _ *mediapb.MediaObjectAuthority) {
			t.EffectiveClusterGrants = append(t.EffectiveClusterGrants, proto.CloneOf(t.EffectiveClusterGrants[0]))
		},
	} {
		t.Run(name, func(t *testing.T) {
			tenant, object := objectCommercialFixture(t)
			mutate(tenant, object)
			if quote, err := PlacementCommercialQuote(tenant, object, placement.Serve, fixtureNow); err == nil || quote != nil {
				t.Fatal("quote reused for different effective policy or entitled census")
			}
		})
	}
}
