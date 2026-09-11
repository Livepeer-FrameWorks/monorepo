package placement

import "testing"

func TestRequiresCommercialFacts(t *testing.T) {
	charging := Selector{Charging: []Charging{Rated}}
	for _, test := range []struct {
		name   string
		policy *Policy
		want   bool
	}{
		{name: "default"},
		{name: "deny all", policy: &Policy{SchemaVersion: SchemaVersion}},
		{name: "own capacity geo spill", policy: &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "own", Match: Selector{Classes: []Class{Private}}, Order: DistanceFirst, Spillover: CapacityOrGeoHole, GeoHoleDistanceKM: 1000, MinImprovementKM: 100}}}},
		{name: "exclude official", policy: &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{Classes: []Class{Official}}}}}}},
		{name: "inherited allow", policy: &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{}, {Allow: &SelectorSet{Any: []Selector{{ClusterIDs: []string{"own"}}, charging}}}}}, want: true},
		{name: "inherited deny", policy: &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{charging}}, {}}}, want: true},
		{name: "charging preference", policy: &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "free", Match: Selector{Charging: []Charging{PermanentlyFree}}}}}, want: true},
		{name: "price ordering", policy: &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "cheap", Order: PriceFirst, PriceCurrency: "USD", PriceUnit: "viewer_minute"}}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.policy); err != nil {
				t.Fatal(err)
			}
			if got := RequiresCommercialFacts(test.policy); got != test.want {
				t.Fatalf("RequiresCommercialFacts = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRequiresCommercialFactsAfterPreferenceReplacement(t *testing.T) {
	tenant := &Rules{SchemaVersion: SchemaVersion, Preferences: &Preferences{Groups: []Group{{ID: "free", Match: Selector{Charging: []Charging{PermanentlyFree}}}}}}
	stream := &Rules{SchemaVersion: SchemaVersion, Preferences: &Preferences{Groups: []Group{{ID: "near", Order: DistanceFirst}}}}
	policy, err := Compile(tenant, stream)
	if err != nil || RequiresCommercialFacts(policy) {
		t.Fatalf("replaced preference still requires quotes: %v", err)
	}
	tenant.Constraints.Deny = []Selector{{Charging: []Charging{Rated}}}
	policy, err = Compile(tenant, stream)
	if err != nil || !RequiresCommercialFacts(policy) {
		t.Fatalf("stream preference erased inherited charging constraint: %v", err)
	}
}
