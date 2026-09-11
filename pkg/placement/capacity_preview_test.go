package placement

import (
	"reflect"
	"testing"
	"time"
)

func TestCapacityPreviewDoesNotInventLiveSource(t *testing.T) {
	candidate := edge("us", "official-us", 38.9, -77)
	candidate.Presence, candidate.SourceFeasible = "", false
	r := request(candidate)
	preview, err := EvaluateCapacity(r)
	if err != nil || len(preview.Choices) != 1 || preview.Choices[0].NodeID != "us" {
		t.Fatalf("observed capacity preview: %+v %v", preview, err)
	}
	if !reflect.DeepEqual(r.Candidates[0], candidate) {
		t.Fatal("preview changed input source facts")
	}
	live, err := Evaluate(r)
	if err != nil || len(live.Choices) != 0 || live.Assessments[0].Reason != NoSourcePath {
		t.Fatalf("preview weakened live source gating: %+v %v", live, err)
	}
}

func TestCapacityPreviewComparisonUsesFreshDetachedBasis(t *testing.T) {
	candidate := edge("node", "cluster", 39, -77)
	candidate.Prices = []Price{{AmountMicros: 7, Currency: "EUR", Unit: "serve:minutes=1;gib=1", Revision: "quote", ExpiresAt: testNow.Add(time.Second)}}
	group := Group{ID: "cheap", Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=1"}
	price := PreviewComparisonPrice(candidate, group, testNow)
	if price == nil || price.AmountMicros != 7 {
		t.Fatal("comparison basis lost")
	}
	price.AmountMicros = 0
	if candidate.Prices[0].AmountMicros != 7 {
		t.Fatal("preview price aliases source evidence")
	}
	if PreviewComparisonPrice(candidate, group, testNow.Add(time.Second)) != nil {
		t.Fatal("expired price projected")
	}
	group.Order = DistanceFirst
	if PreviewComparisonPrice(candidate, group, testNow) != nil {
		t.Fatal("distance ordering invented a comparison basis")
	}
}

func TestCapacityPreviewIgnoresUnevaluatedPresence(t *testing.T) {
	a, b := edge("a", "official", 38.9, -77), edge("b", "official", 38.9, -77)
	a.Presence, a.SourceFeasible = "", false
	r := request(a, b)
	before, err := EvaluateCapacity(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Candidates[0].Presence, r.Candidates[0].SourceFeasible = Present, true
	r.Candidates[1].Presence, r.Candidates[1].SourceFeasible = "", false
	after, err := EvaluateCapacity(r)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("unevaluated source affected capacity ranking: %+v %+v %v", before, after, err)
	}
}

func TestCapacityPreviewPreservesNonSourceRestrictions(t *testing.T) {
	for _, scenario := range []string{"entitlement", "ownership", "deny", "unknown capacity", "stale", "metrics", "full", "geo", "price"} {
		t.Run(scenario, func(t *testing.T) {
			r := request(edge("node", "cluster", 38.9, -77))
			c := &r.Candidates[0]
			want := NotEntitled
			switch scenario {
			case "entitlement":
				c.AllowedVerbs = []Verb{Ingest}
			case "ownership":
				c.Official, c.OwnerTenantID = false, ""
				want = UnknownOwnership
			case "deny":
				r.Policy = &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{Classes: []Class{Official}}}}}}
				want = PolicyDenied
			case "unknown capacity":
				c.Capacity, want = CapacityUnknown, UnknownCapacity
			case "stale":
				c.ExpiresAt, want = r.Now.Add(-time.Second), StaleTelemetry
			case "metrics":
				c.RAMMax, want = 0, InvalidMetrics
			case "full":
				c.Capacity, want = CapacityExhausted, CapacityFull
			case "geo":
				r.Policy = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "near", MaxDistanceKM: 1}}}
				r.Location = &Coordinates{Latitude: 52, Longitude: 5}
				want = OutsideGeoBound
			case "price":
				r.Policy = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "cheap", Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "serve:minutes=1;gib=1"}}}
				want = PriceUnavailable
			}
			out, err := EvaluateCapacity(r)
			if err != nil || len(out.Choices) != 0 || len(out.Assessments) != 1 || out.Assessments[0].Reason != want {
				t.Fatalf("capacity preview bypassed %s: %+v %v", scenario, out, err)
			}
		})
	}
}

func TestCapacityPreviewRetainsVerifiedSpillAndIngestFence(t *testing.T) {
	r := request(ownedEU(), edge("us", "official", 38.9, -77))
	r.Policy = privateFirst(CapacityOnly)
	r.Candidates[0].Capacity = CapacityUnknown
	out, err := EvaluateCapacity(r)
	if err != nil || len(out.Choices) != 0 {
		t.Fatalf("unknown preferred capacity permitted spill: %+v %v", out, err)
	}
	r.Candidates[0].Capacity = CapacityExhausted
	out, err = EvaluateCapacity(r)
	if err != nil || len(out.Choices) != 1 || out.Choices[0].NodeID != "us" {
		t.Fatalf("verified exhaustion did not permit spill: %+v %v", out, err)
	}
	r.Verb, r.ActiveIngestClusterID = Ingest, r.Candidates[0].ClusterID
	out, err = EvaluateCapacity(r)
	if err != nil || len(out.Choices) != 0 {
		t.Fatalf("preview moved an active ingest owner: %+v %v", out, err)
	}
}
