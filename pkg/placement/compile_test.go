package placement

import (
	"math"
	"testing"
)

func TestCompileRetainsTenantConstraintsAndReplacesPreferences(t *testing.T) {
	tenant := &Rules{SchemaVersion: SchemaVersion, Constraints: Constraints{Deny: []Selector{{Classes: []Class{Official}}}}, Preferences: &Preferences{Groups: []Group{{ID: "own", Match: Selector{Classes: []Class{Private}}}}}}
	stream := &Rules{SchemaVersion: SchemaVersion, Preferences: &Preferences{Groups: []Group{{ID: "all"}}}}
	p, err := Compile(tenant, stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Groups) != 1 || p.Groups[0].ID != "all" {
		t.Fatalf("preferences not replaced: %+v", p)
	}
	r := request(ownedEU(), edge("us", "official-us", 38.9, -77))
	r.Policy = p
	if winner(evaluate(t, r)) != "owned-eu" {
		t.Fatal("overlay erased tenant deny")
	}
	stream.Preferences.Groups[0].ID = "mutated"
	tenant.Constraints.Deny[0].Classes[0] = Private
	if p.Groups[0].ID != "all" || winner(evaluate(t, r)) != "owned-eu" {
		t.Fatal("compiled policy aliases editable intent")
	}
}

func TestCompileInheritanceAndExplicitEmpty(t *testing.T) {
	if p, err := Compile(nil, nil); err != nil || p != nil {
		t.Fatalf("absence: %v %v", p, err)
	}
	tenant := &Rules{SchemaVersion: SchemaVersion, Preferences: &Preferences{Groups: []Group{{ID: "own"}}}}
	overlay := &Rules{SchemaVersion: SchemaVersion}
	p, err := Compile(tenant, overlay)
	if err != nil || len(p.Groups) != 1 || p.Groups[0].ID != "own" {
		t.Fatalf("inheritance: %v %v", p, err)
	}
	overlay.Preferences = &Preferences{}
	p, err = Compile(tenant, overlay)
	if err != nil || len(p.Groups) != 0 {
		t.Fatalf("empty overlay widened policy: %v %v", p, err)
	}
	overlay.SchemaVersion = 2
	if _, err := Compile(tenant, overlay); err == nil {
		t.Fatal("unknown overlay version accepted")
	}
}

func TestCompileRejectsInvalidOverriddenPreferences(t *testing.T) {
	tenant := &Rules{SchemaVersion: SchemaVersion, Preferences: &Preferences{Groups: []Group{{ID: "bad", Spillover: "unknown"}}}}
	stream := &Rules{SchemaVersion: SchemaVersion, Preferences: &Preferences{Groups: []Group{{ID: "valid"}}}}
	if _, err := Compile(tenant, stream); err == nil {
		t.Fatal("invalid stored rules hidden by overlay")
	}
}

func TestDigestCanonicalSetsButPreservesPriorityAndDenyAll(t *testing.T) {
	a := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{ClusterIDs: []string{"b", "a", "a"}}, {Classes: []Class{Official}}}}}, Groups: []Group{{ID: "all", MaxDistanceKM: math.Copysign(0, -1)}}}
	b := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{Classes: []Class{Official}}, {ClusterIDs: []string{"a", "b"}}}}}, Groups: []Group{{ID: "all", Spillover: Never, Order: DistanceFirst}}}
	da, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(b)
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Fatalf("set order/default representation changed digest: %s %s", da, db)
	}
	if a.Layers[0].Deny[0].ClusterIDs[0] != "b" || a.Groups[0].Spillover != "" {
		t.Fatal("digest mutated input")
	}
	b.Layers[0].Allow = &SelectorSet{}
	db, _ = Digest(b)
	if da == db {
		t.Fatal("empty allow indistinguishable from absent allow")
	}
	a.Groups = []Group{{ID: "one"}, {ID: "two"}}
	b = canonicalCopy(a)
	b.Groups[0], b.Groups[1] = b.Groups[1], b.Groups[0]
	da, _ = Digest(a)
	db, _ = Digest(b)
	if da == db {
		t.Fatal("group order missing from digest")
	}
}
