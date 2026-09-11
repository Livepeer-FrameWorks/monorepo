package placement

import (
	"reflect"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCandidateObservationWireRoundTrip(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, verb := range []Verb{Ingest, Serve} {
		original := Candidate{
			TenantID: "tenant", ClusterID: "cluster", NodeID: "node", OwnerTenantID: "tenant", Region: "us-east",
			AllowedVerbs: []Verb{Ingest, Serve}, Charging: Rated, ChargingRevision: "price-1", ChargingUntil: now.Add(time.Hour),
			Location: &Coordinates{}, ObservedAt: now, ExpiresAt: now.Add(time.Second), Capacity: CapacityAvailable,
			BWAvailable: 1<<63 + 17, BWLimit: 1<<63 + 19, CPUPercent: 12.5, RAMUsed: 1<<54 + 3, RAMMax: 1<<54 + 7,
			Presence: Absent, SourceFeasible: true, Price: &Price{AmountMicros: 1<<54 + 3, Currency: "USD", Unit: "GB", Revision: "price-1", ExpiresAt: now.Add(time.Hour)},
			Prices: []Price{{AmountMicros: 1<<63 + 19, Currency: "EUR", Unit: "serve:minutes=60;gib=2", Revision: "price-1", ExpiresAt: now.Add(time.Hour)},
				{AmountMicros: 9, Currency: "USD", Unit: "serve:minutes=1;gib=0", Revision: "price-1", ExpiresAt: now.Add(time.Hour)}},
		}
		wire, err := CandidateToProto(original, verb)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := proto.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		var decoded placementpb.ObservedCandidate
		if decodeErr := proto.Unmarshal(encoded, &decoded); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		actual, err := CandidateFromProto(&decoded, verb)
		if err != nil || !reflect.DeepEqual(actual, original) {
			t.Fatalf("lost typed observation precision/presence: %+v\n%+v\n%v", actual, original, err)
		}
		other := Serve
		if verb == Serve {
			other = Ingest
		}
		wrongVerb, err := CandidateFromProto(&decoded, other)
		if err != nil || wrongVerb.Price != nil || len(wrongVerb.Prices) != 0 {
			t.Fatal("price for another verb reused")
		}
		actual.Prices[0].AmountMicros = 1
		wirePrices := decoded.GetCommercialFacts().GetServePrices()
		if verb == Ingest {
			wirePrices = decoded.GetCommercialFacts().GetIngestPrices()
		}
		if original.Prices[0].AmountMicros == 1 || wirePrices[0].GetAmountMicros() == 1 {
			t.Fatal("candidate aliases original or wire prices")
		}
	}
}

func TestCandidateObservationWirePreservesUnknown(t *testing.T) {
	unknown, err := CandidateFromProto(&placementpb.ObservedCandidate{}, Serve)
	if err != nil || unknown.Location != nil || !unknown.ObservedAt.IsZero() || unknown.Capacity != CapacityUnknown || unknown.Charging != ChargingUnknown || unknown.Price != nil || unknown.Presence != "" {
		t.Fatalf("invented observation facts: %+v %v", unknown, err)
	}
}

func TestCandidateObservationWireRejectsUnsupportedFacts(t *testing.T) {
	for name, wire := range map[string]*placementpb.ObservedCandidate{
		"capacity":         {Capacity: 99},
		"presence":         {Presence: 99},
		"verb":             {AllowedVerbs: []placementpb.Verb{99}},
		"charging":         {CommercialFacts: &placementpb.CommercialFacts{Charging: 99}},
		"observation_time": {ObservedAt: &timestamppb.Timestamp{Nanos: -1}},
		"expiry":           {ExpiresAt: &timestamppb.Timestamp{Seconds: 1 << 62}},
		"price_expiry":     {CommercialFacts: &placementpb.CommercialFacts{ExpiresAt: &timestamppb.Timestamp{Nanos: -1}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CandidateFromProto(wire, Serve); err == nil {
				t.Fatal("unsupported observation accepted")
			}
		})
	}
	for _, nested := range []bool{false, true} {
		wire := &placementpb.ObservedCandidate{CommercialFacts: &placementpb.CommercialFacts{}}
		if nested {
			wire.CommercialFacts.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
		} else {
			wire.ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
		}
		if _, err := CandidateFromProto(wire, Serve); err == nil {
			t.Fatal("unknown observation fields accepted")
		}
	}
	if _, err := CandidateToProto(Candidate{Capacity: CapacityUnknown, Price: &Price{Revision: "different"}}, Serve); err == nil {
		t.Fatal("mismatched commercial revision accepted")
	}
}
