package placement

import (
	"math"
	"reflect"
	"slices"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)

func edge(node, cluster string, lat, lon float64) Candidate {
	return Candidate{
		TenantID: "tenant", NodeID: node, ClusterID: cluster, Official: true,
		AllowedVerbs: []Verb{Ingest, Serve}, Charging: Rated, ChargingUntil: testNow.Add(time.Minute),
		Location: &Coordinates{Latitude: lat, Longitude: lon}, ObservedAt: testNow.Add(-time.Second), ExpiresAt: testNow.Add(time.Minute),
		Capacity: CapacityAvailable, BWAvailable: 800, BWLimit: 1000, CPUPercent: 10, RAMUsed: 100, RAMMax: 1000,
		Presence: Present, SourceFeasible: true,
	}
}

func request(candidates ...Candidate) Request {
	return Request{TenantID: "tenant", Verb: Serve, Now: testNow, Location: &Coordinates{Latitude: 38.9, Longitude: -77}, Candidates: candidates, Complete: true}
}

func privateFirst(spill Spillover) *Policy {
	g := Group{ID: "own", Match: Selector{Classes: []Class{Private}}, Spillover: spill}
	if spill == GeoHole || spill == CapacityOrGeoHole {
		g.GeoHoleDistanceKM, g.MinImprovementKM = 2500, 500
	}
	return &Policy{SchemaVersion: SchemaVersion, Groups: []Group{g, {ID: "official", Match: Selector{Classes: []Class{Official}}}}}
}

func ownedEU() Candidate {
	c := edge("owned-eu", "private-eu", 52.4, 4.9)
	c.Official = false
	c.OwnerTenantID = "tenant"
	return c
}

func evaluate(t *testing.T, r Request) Decision {
	t.Helper()
	d, err := Evaluate(r)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func winner(d Decision) string {
	if len(d.Choices) == 0 {
		return ""
	}
	return d.Choices[0].NodeID
}

func TestServingDestinationIndependentOfInputOrderAndSourcePresence(t *testing.T) {
	us := edge("us", "us-cell", 38.9, -77)
	us.Presence = Absent
	eu := edge("eu", "eu-cell", 52.4, 4.9)
	third := edge("asia", "asia-cell", 35, 139)
	for _, input := range [][]Candidate{{us, eu, third}, {eu, us, third}, {third, eu, us}, {third, us, eu}} {
		r := request(input...)
		d := evaluate(t, r)
		if winner(d) != "us" || !d.Choices[0].RequiresPull {
			t.Fatalf("wrong serving destination/action: %+v", d)
		}
		if d.Choices[1].NodeID != "eu" {
			t.Fatalf("wrong fallback order: %+v", d.Choices)
		}
	}
	// Neither a shared control cell nor a shared virtual cluster changes locality.
	us.ClusterID = eu.ClusterID
	if winner(evaluate(t, request(eu, us))) != "us" {
		t.Fatal("cluster boundary changed destination")
	}
}

func TestMissingHardFactsCannotAuthorizeSpill(t *testing.T) {
	for _, missing := range []string{"ownership", "charging"} {
		t.Run(missing, func(t *testing.T) {
			own := ownedEU()
			r := request(own, edge("us", "official-us", 38.9, -77))
			r.Policy = privateFirst(CapacityOrGeoHole)
			if missing == "ownership" {
				r.Candidates[0].OwnerTenantID = ""
			} else {
				r.Policy.Layers = []Constraints{{Deny: []Selector{{Charging: []Charging{Rated}, Classes: []Class{Private}}}}}
				r.Candidates[0].Charging = ChargingUnknown
			}
			if d := evaluate(t, r); winner(d) != "" || len(d.Transitions) != 0 {
				t.Fatalf("missing %s caused fallback: %+v", missing, d)
			}
		})
	}
}

func TestCanonicalPolicyHasIdenticalDecisions(t *testing.T) {
	r := request(edge("us", "official-us", 38.9, -77))
	r.Candidates[0].Charging = ChargingUnknown
	r.Policy = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "any"}}, Layers: []Constraints{
		{Deny: []Selector{{Charging: []Charging{Rated}}}},
		{Deny: []Selector{{Classes: []Class{Official}}}},
	}}
	original := evaluate(t, r)
	r.Policy = canonicalCopy(r.Policy)
	if canonical := evaluate(t, r); !reflect.DeepEqual(original, canonical) {
		t.Fatalf("canonicalization changed decision: %+v != %+v", original, canonical)
	}
}

func TestUnknownSelectorFactsFailClosed(t *testing.T) {
	for _, selector := range []Selector{{Regions: []string{"eu"}}, {OwnerIDs: []string{"operator"}}} {
		for _, allow := range []bool{false, true} {
			r := request(edge("us", "official-us", 38.9, -77))
			layer := Constraints{Deny: []Selector{selector}}
			if allow {
				layer = Constraints{Allow: &SelectorSet{Any: []Selector{selector}}}
			}
			r.Policy = &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{layer}, Groups: []Group{{ID: "all"}}}
			d := evaluate(t, r)
			if winner(d) != "" || d.Assessments[0].Reason != PolicyFactsUnavailable {
				t.Fatalf("unknown selector facts bypassed constraints: %+v", d)
			}
		}
	}
}

func TestPrivateFirstSpillover(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spill      Spillover
		edit       func(*Request)
		want       string
		transition Reason
	}{
		{"prefer owned even far", Never, nil, "owned-eu", ""},
		{"capacity only does not optimize geo", CapacityOnly, nil, "owned-eu", ""},
		{"geo hole", GeoHole, nil, "us", GeographicHole},
		{"capacity or geo", CapacityOrGeoHole, nil, "us", GeographicHole},
		{"full spills", CapacityOnly, func(r *Request) { r.Candidates[0].Capacity = CapacityExhausted }, "us", PreferredCapacityExhausted},
		{"zero available spills", CapacityOnly, func(r *Request) { r.Candidates[0].BWAvailable = 0 }, "us", PreferredCapacityExhausted},
		{"never stays unavailable", Never, func(r *Request) { r.Candidates[0].Capacity = CapacityExhausted }, "", ""},
		{"geo alone not capacity", GeoHole, func(r *Request) { r.Candidates[0].Capacity = CapacityExhausted }, "", ""},
		{"stale is not full", CapacityOnly, func(r *Request) { r.Candidates[0].ExpiresAt = testNow }, "", ""},
		{"unreachable is not full", CapacityOnly, func(r *Request) { r.Candidates[0].Capacity = CapacityUnavailable }, "", ""},
		{"unknown is not full", CapacityOnly, func(r *Request) { r.Candidates[0].Capacity = CapacityUnknown }, "", ""},
		{"partial full pool is not proof", CapacityOnly, func(r *Request) { r.Candidates[0].Capacity = CapacityExhausted; r.Complete = false }, "", ""},
		{"partial geo not proof", GeoHole, func(r *Request) { r.Complete = false }, "owned-eu", ""},
		{"unknown viewer geo", GeoHole, func(r *Request) { r.Location = nil }, "owned-eu", ""},
		{"unknown edge geo", GeoHole, func(r *Request) { r.Candidates[0].Location = nil }, "owned-eu", ""},
		{"fallback unknown geo", GeoHole, func(r *Request) { r.Candidates[1].Location = nil }, "owned-eu", ""},
		{"fallback not closer", GeoHole, func(r *Request) { r.Candidates[1].Location = &Coordinates{Latitude: 35, Longitude: 139} }, "owned-eu", ""},
		{"fallback must improve enough", GeoHole, func(r *Request) { r.Candidates[1].Location = r.Candidates[0].Location }, "owned-eu", ""},
		{"empty complete owned pool", CapacityOnly, func(r *Request) { r.Candidates = r.Candidates[1:] }, "us", PreferredCapacityExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(ownedEU(), edge("us", "official-us", 38.9, -77))
			r.Policy = privateFirst(tc.spill)
			if tc.edit != nil {
				tc.edit(&r)
			}
			d := evaluate(t, r)
			if winner(d) != tc.want {
				t.Fatalf("winner=%q want=%q decision=%+v", winner(d), tc.want, d)
			}
			if tc.transition == "" && len(d.Transitions) != 0 {
				t.Fatalf("unexpected spill: %+v", d.Transitions)
			}
			if tc.transition != "" && (len(d.Transitions) != 1 || d.Transitions[0].Reason != tc.transition) {
				t.Fatalf("wrong transition: %+v", d.Transitions)
			}
		})
	}
}

func TestHardRulesCannotBeOverriddenByPreferences(t *testing.T) {
	for _, tc := range []struct {
		name   string
		layers []Constraints
		want   string
	}{
		{"official denied", []Constraints{{Deny: []Selector{{Classes: []Class{Official}}}}}, "owned-eu"},
		{"overlay allow cannot erase deny", []Constraints{{Deny: []Selector{{Classes: []Class{Official}}}}, {Allow: &SelectorSet{Any: []Selector{{Classes: []Class{Official}}}}}}, ""},
		{"allow intersection", []Constraints{{Allow: &SelectorSet{Any: []Selector{{Classes: []Class{Private}}}}}, {Allow: &SelectorSet{Any: []Selector{{ClusterIDs: []string{"official-us"}}}}}}, ""},
		{"explicit empty allow", []Constraints{{Allow: &SelectorSet{}}}, ""},
		{"no extra allow", []Constraints{{Allow: nil}}, "us"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(ownedEU(), edge("us", "official-us", 38.9, -77))
			r.Policy = &Policy{SchemaVersion: SchemaVersion, Layers: tc.layers, Groups: []Group{{ID: "all"}}}
			if d := evaluate(t, r); winner(d) != tc.want {
				t.Fatalf("got %+v", d)
			}
		})
	}
	r := request(edge("us", "official-us", 38.9, -77))
	r.Policy = &Policy{SchemaVersion: SchemaVersion}
	if len(evaluate(t, r).Choices) != 0 {
		t.Fatal("explicit empty groups must deny")
	}
	r.Policy = nil
	if winner(evaluate(t, r)) != "us" {
		t.Fatal("absent policy must retain entitled default")
	}
}

func TestNeverRatedOfficialFailsClosedOnUnknownCharging(t *testing.T) {
	for _, tc := range []struct {
		name     string
		charging Charging
		until    time.Time
		want     string
	}{
		{"rated", Rated, testNow.Add(time.Minute), "owned-eu"},
		{"free", PermanentlyFree, testNow.Add(time.Minute), "us"},
		{"unknown", ChargingUnknown, testNow.Add(time.Minute), "owned-eu"},
		{"expired free", PermanentlyFree, testNow, "owned-eu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			us := edge("us", "official-us", 38.9, -77)
			us.Charging, us.ChargingUntil = tc.charging, tc.until
			r := request(us, ownedEU())
			r.Policy = &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{Classes: []Class{Official}, Charging: []Charging{Rated}}}}}, Groups: []Group{{ID: "all"}}}
			if d := evaluate(t, r); winner(d) != tc.want {
				t.Fatalf("got %+v", d)
			}
		})
	}
}

func TestCandidateAuthorizationAndFeasibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*Candidate)
		reason Reason
	}{
		{"wrong tenant", func(c *Candidate) { c.TenantID = "other" }, NotEntitled},
		{"wrong verb", func(c *Candidate) { c.AllowedVerbs = []Verb{Ingest} }, NotEntitled},
		{"unknown ownership", func(c *Candidate) { c.Official = false }, UnknownOwnership},
		{"future observation", func(c *Candidate) { c.ObservedAt = testNow.Add(time.Second) }, StaleTelemetry},
		{"absent observation", func(c *Candidate) { c.ObservedAt = time.Time{} }, StaleTelemetry},
		{"missing capacity", func(c *Candidate) { c.Capacity = "" }, UnknownCapacity},
		{"CPU NaN", func(c *Candidate) { c.CPUPercent = math.NaN() }, InvalidMetrics},
		{"negative CPU", func(c *Candidate) { c.CPUPercent = -1 }, InvalidMetrics},
		{"infinite CPU", func(c *Candidate) { c.CPUPercent = math.Inf(1) }, InvalidMetrics},
		{"oversized CPU", func(c *Candidate) { c.CPUPercent = 101 }, InvalidMetrics},
		{"missing RAM", func(c *Candidate) { c.RAMMax = 0 }, InvalidMetrics},
		{"RAM over limit", func(c *Candidate) { c.RAMUsed = c.RAMMax + 1 }, InvalidMetrics},
		{"BW over limit", func(c *Candidate) { c.BWAvailable = c.BWLimit + 1 }, InvalidMetrics},
		{"missing BW maximum", func(c *Candidate) { c.BWLimit = 0 }, InvalidMetrics},
		{"no source", func(c *Candidate) { c.Presence = Absent; c.SourceFeasible = false }, NoSourcePath},
		{"materializing has no source", func(c *Candidate) { c.Presence = Materializing; c.SourceFeasible = false }, NoSourcePath},
		{"unknown presence", func(c *Candidate) { c.Presence = "" }, NoSourcePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := edge("us", "official-us", 38.9, -77)
			tc.edit(&c)
			d := evaluate(t, request(c))
			if len(d.Choices) != 0 || len(d.Assessments) != 1 || d.Assessments[0].Reason != tc.reason {
				t.Fatalf("got %+v want rejection %s", d, tc.reason)
			}
		})
	}
}

func TestIngestFenceDoesNotGrantOrMigrate(t *testing.T) {
	r := request(ownedEU(), edge("us", "official-us", 38.9, -77))
	r.Verb = Ingest
	r.ActiveIngestClusterID = "private-eu"
	if d := evaluate(t, r); winner(d) != "owned-eu" || d.Choices[0].RequiresPull {
		t.Fatalf("claim moved: %+v", d)
	}
	r.Candidates[0].AllowedVerbs = nil
	if len(evaluate(t, r).Choices) != 0 {
		t.Fatal("claim granted access or changed owner")
	}
}

func TestHeadroomNormalizationAndStableTies(t *testing.T) {
	a, b := edge("a", "a", 0, 0), edge("b", "b", 0, 0)
	a.BWLimit, a.BWAvailable = 100, 90
	b.BWLimit, b.BWAvailable = 10000, 5000
	if winner(evaluate(t, request(b, a))) != "a" {
		t.Fatal("absolute bandwidth biased relative headroom")
	}
	a.BWLimit, a.BWAvailable = math.MaxUint64, math.MaxUint64-1
	b.BWLimit, b.BWAvailable = math.MaxUint64, math.MaxUint64-2
	if winner(evaluate(t, request(b, a))) != "a" {
		t.Fatal("large byte counters overflowed comparison")
	}
	b.BWAvailable = a.BWAvailable
	if winner(evaluate(t, request(b, a))) != "a" {
		t.Fatal("tie is not stable")
	}
	if compareFraction(1, 2, 100, 200) != 0 {
		t.Fatal("equal normalized headroom differs")
	}
}

func TestPriceOrderingRequiresComparableFreshBasis(t *testing.T) {
	a, b := ownedEU(), edge("us", "official-us", 38.9, -77)
	a.Price = &Price{AmountMicros: 10, Currency: "EUR", Unit: "delivery_GiB", Revision: "1", ExpiresAt: testNow.Add(time.Minute)}
	b.Price = &Price{AmountMicros: 20, Currency: "EUR", Unit: "delivery_GiB", Revision: "2", ExpiresAt: testNow.Add(time.Minute)}
	r := request(a, b)
	r.Policy = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "cost", Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "delivery_GiB", MaxDistanceKM: 10000}}}
	if winner(evaluate(t, r)) != "owned-eu" {
		t.Fatal("explicit price order ignored")
	}
	for _, price := range []*Price{nil, {Currency: "USD", Unit: "delivery_GiB", Revision: "1", ExpiresAt: testNow.Add(time.Minute)}, {Currency: "EUR", Unit: "minutes", Revision: "1", ExpiresAt: testNow.Add(time.Minute)}, {Currency: "EUR", Unit: "delivery_GiB", Revision: "1", ExpiresAt: testNow}} {
		r.Candidates[0].Price = price
		if d := evaluate(t, r); winner(d) != "us" {
			t.Fatalf("unknown/incomparable price treated as free: %+v", d)
		}
	}
	r.Candidates[0].Price = a.Price
	r.Policy.Groups[0].MaxDistanceKM = 100
	if winner(evaluate(t, r)) != "us" {
		t.Fatal("price bypassed geographic bound")
	}
}

func TestPolicyValidation(t *testing.T) {
	for _, p := range []*Policy{
		{SchemaVersion: 2},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "a"}, {ID: "a"}}},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: ""}}},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "a", Spillover: "optimize"}}},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "a", Spillover: GeoHole}}},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "a", Order: PriceFirst}}},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "a", MaxDistanceKM: math.NaN()}}},
		{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "a", Match: Selector{Charging: []Charging{ChargingUnknown}}}}},
		{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{Classes: []Class{"other"}}}}}},
	} {
		if err := Validate(p); err == nil {
			t.Fatalf("accepted malformed policy %+v", p)
		}
	}
	r := request(edge("a", "a", 0, 0))
	r.Candidates = append(r.Candidates, r.Candidates[0])
	if _, err := Evaluate(r); err == nil {
		t.Fatal("accepted duplicate identity")
	}
	r = request()
	r.Location = &Coordinates{Latitude: math.NaN()}
	if _, err := Evaluate(r); err == nil {
		t.Fatal("accepted invalid request geo")
	}
}

func TestFirstMatchingGroupOwnsCandidate(t *testing.T) {
	r := request(ownedEU())
	r.Policy = &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "first", Spillover: GeoHole, GeoHoleDistanceKM: 1, MinImprovementKM: 1}, {ID: "second"}}}
	d := evaluate(t, r)
	if winner(d) != "owned-eu" || d.Choices[0].GroupID != "first" || len(d.Transitions) != 0 {
		t.Fatalf("overlap produced fictitious spill: %+v", d)
	}
}

func TestMultiGroupSpillPreservesGeoImprovement(t *testing.T) {
	r := request(ownedEU(), edge("us", "official-us", 38.9, -77))
	r.Policy = privateFirst(GeoHole)
	r.Policy.Groups = append(r.Policy.Groups[:1], Group{ID: "market", Match: Selector{Classes: []Class{Marketplace}}, Spillover: CapacityOnly}, r.Policy.Groups[1])
	d := evaluate(t, r)
	if winner(d) != "us" || len(d.Transitions) != 2 {
		t.Fatalf("nested spill failed: %+v", d)
	}
	r.Candidates[1].Location = r.Candidates[0].Location
	if d = evaluate(t, r); winner(d) != "owned-eu" || len(d.Transitions) != 0 {
		t.Fatalf("lost original geo constraint: %+v", d)
	}
}

func FuzzCandidatePermutation(f *testing.F) {
	f.Add(uint64(1000), uint64(100), 40.0)
	f.Add(uint64(math.MaxUint64), uint64(math.MaxUint64-1), 0.0)
	f.Fuzz(func(t *testing.T, limit, available uint64, latitude float64) {
		if !finite(latitude) {
			return
		}
		a, b := edge("a", "a", math.Mod(latitude, 90), 0), edge("b", "b", 38.9, -77)
		a.BWLimit, a.BWAvailable = limit, available
		r := request(a, b)
		before := slices.Clone(r.Candidates)
		first := evaluate(t, r)
		if !reflect.DeepEqual(before, r.Candidates) {
			t.Fatal("mutated input candidates")
		}
		slices.Reverse(r.Candidates)
		second := evaluate(t, r)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("input order affected decision: %+v / %+v", first, second)
		}
	})
}
