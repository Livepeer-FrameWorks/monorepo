package placement

import (
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestCommercialPriceCollectionsRejectAmbiguity(t *testing.T) {
	quote := func() *placementpb.Price {
		return &placementpb.Price{Currency: "EUR", Unit: "serve:minutes=1;gib=2", AmountMicros: 20}
	}
	for name, facts := range map[string]*placementpb.CommercialFacts{
		"nil entry":          {ServePrices: []*placementpb.Price{nil}},
		"repeat":             {ServePrices: []*placementpb.Price{quote(), quote()}},
		"singular collision": {ServePrice: quote(), ServePrices: []*placementpb.Price{quote()}},
		"ingest collision":   {IngestPrice: quote(), IngestPrices: []*placementpb.Price{quote()}},
		"unbounded":          {ServePrices: make([]*placementpb.Price, MaxPricesPerVerb+1)},
		"false free":         {Charging: placementpb.Charging_CHARGING_PERMANENTLY_FREE, ServePrices: []*placementpb.Price{quote()}},
		"currency":           {ServePrices: []*placementpb.Price{{Currency: "eur", Unit: "minute"}}},
		"unit control":       {ServePrices: []*placementpb.Price{{Currency: "EUR", Unit: "min\x00ute"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCommercialPrices(facts); err == nil {
				t.Fatal("invalid collection accepted")
			}
			if _, err := CandidateFromProto(&placementpb.ObservedCandidate{CommercialFacts: facts}, Ingest); err == nil {
				t.Fatal("malformed unselected verb escaped observation validation")
			}
		})
	}
	facts := &placementpb.CommercialFacts{IngestPrices: []*placementpb.Price{quote()}, ServePrices: []*placementpb.Price{quote()}}
	if err := ValidateCommercialPrices(facts); err != nil {
		t.Fatalf("distinct verbs share an independent basis: %v", err)
	}
	facts.ServePrices[0].ProtoReflect().SetUnknown([]byte{0xf8, 0x07, 0x01})
	if err := ValidateCommercialPrices(facts); err == nil {
		t.Fatal("unknown nested quote fields accepted")
	}
	facts = &placementpb.CommercialFacts{ServePrice: quote()}
	for i := 1; i < MaxPricesPerVerb; i++ {
		facts.ServePrices = append(facts.ServePrices, &placementpb.Price{Currency: "EUR", Unit: "serve:minutes=" + strconv.Itoa(i) + ";gib=0"})
	}
	if err := ValidateCommercialPrices(facts); err != nil {
		t.Fatalf("maximum-sized distinct collection rejected: %v", err)
	}
	facts.ServePrices = append(facts.ServePrices, &placementpb.Price{Currency: "EUR", Unit: "serve:minutes=60;gib=2"})
	if err := ValidateCommercialPrices(facts); err == nil {
		t.Fatal("singular slot not included in collection bound")
	}
}

func TestPriceOrderingSelectsEachGroupsOwnBasis(t *testing.T) {
	a, b := ownedEU(), edge("us", "official-us", 38.9, -77)
	price := func(unit string, amount uint64) Price {
		return Price{Currency: "EUR", Unit: unit, AmountMicros: amount, Revision: "quotes-1", ExpiresAt: testNow.Add(time.Minute)}
	}
	a.Prices = []Price{price("serve:minutes=1;gib=0", 10), price("serve:minutes=60;gib=2", 100)}
	b.Prices = []Price{price("serve:minutes=60;gib=2", 20), price("serve:minutes=1;gib=0", 200)}
	original := slices.Clone(a.Prices)
	r := request(a, b)
	r.Policy = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "cost", Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=0"}}}
	if d := evaluate(t, r); winner(d) != a.NodeID {
		t.Fatalf("first basis: %+v", d)
	}
	r.Policy.Groups[0].PriceUnit = "serve:minutes=60;gib=2"
	if d := evaluate(t, r); winner(d) != b.NodeID {
		t.Fatalf("second basis: %+v", d)
	}
	if !reflect.DeepEqual(r.Candidates[0].Prices, original) || r.Candidates[0].Price != nil {
		t.Fatal("selection rewrote candidate quotes")
	}
	r.Policy.Groups = []Group{
		{ID: "own", Match: Selector{Classes: []Class{Private}}, Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=0", Spillover: CapacityOnly},
		{ID: "others", Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=60;gib=2"},
	}
	if d := evaluate(t, r); winner(d) != a.NodeID {
		t.Fatalf("price escaped preferred group: %+v", d)
	}
	r.Candidates[0].Capacity = CapacityExhausted
	if d := evaluate(t, r); winner(d) != b.NodeID || d.Choices[0].GroupID != "others" {
		t.Fatalf("spill did not use next group's own basis: %+v", d)
	}
	r.Candidates[0].Capacity = CapacityAvailable
	r.Candidates[0].Prices = nil
	if d := evaluate(t, r); len(d.Choices) != 0 {
		t.Fatalf("unknown preferred price authorized capacity spill: %+v", d)
	}
}

func TestPriceCollectionDoesNotSelectAroundInvalidFacts(t *testing.T) {
	base := Price{Currency: "EUR", Unit: "minute", Revision: "1", ExpiresAt: testNow.Add(time.Minute), AmountMicros: 1}
	group := Group{PriceCurrency: "EUR", PriceUnit: "minute"}
	for name, mutate := range map[string]func(*Candidate){
		"duplicate":          func(c *Candidate) { c.Prices = append(c.Prices, base) },
		"singular duplicate": func(c *Candidate) { c.Price = &base },
		"stale":              func(c *Candidate) { c.Prices[0].ExpiresAt = testNow },
		"unversioned":        func(c *Candidate) { c.Prices[0].Revision = "" },
		"wrong currency":     func(c *Candidate) { c.Prices[0].Currency = "USD" },
		"too many":           func(c *Candidate) { c.Prices = make([]Price, MaxPricesPerVerb+1) },
	} {
		t.Run(name, func(t *testing.T) {
			c := Candidate{Prices: []Price{base}}
			mutate(&c)
			if comparablePrice(c, group, testNow) != nil {
				t.Fatal("unusable quote selected")
			}
		})
	}
	for _, changeExpiry := range []bool{false, true} {
		c := Candidate{ChargingRevision: "1", ChargingUntil: base.ExpiresAt, Prices: []Price{base}}
		if changeExpiry {
			c.Prices[0].ExpiresAt = base.ExpiresAt.Add(time.Second)
		} else {
			c.Prices[0].Revision = "2"
		}
		if _, err := CandidateToProto(c, Serve); err == nil {
			t.Fatal("collection escaped commercial snapshot binding")
		}
	}
}
