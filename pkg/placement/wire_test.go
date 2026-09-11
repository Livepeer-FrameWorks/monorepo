package placement

import (
	"math"
	"testing"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestRulesWireRoundTripPreservesIntent(t *testing.T) {
	for name, rules := range map[string]*Rules{
		"absent":              nil,
		"inherit preferences": {SchemaVersion: 1, Constraints: Constraints{Deny: []Selector{{Classes: []Class{Official}, Charging: []Charging{Rated}}}}},
		"empty allow":         {SchemaVersion: 1, Constraints: Constraints{Allow: &SelectorSet{}}},
		"empty preferences":   {SchemaVersion: 1, Preferences: &Preferences{}},
		"ordered groups": {SchemaVersion: 1, Preferences: &Preferences{Groups: []Group{
			{ID: "private", Match: Selector{OwnerIDs: []string{"owner-b", "owner-a", "owner-a"}}, Spillover: CapacityOrGeoHole, GeoHoleDistanceKM: 1500, MinImprovementKM: 500},
			{ID: "market", Match: Selector{Classes: []Class{Marketplace}}, Order: PriceFirst, PriceCurrency: "EUR", PriceUnit: "delivered_minute"},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			wire, err := RulesToProto(rules)
			if err != nil {
				t.Fatal(err)
			}
			if wire != nil {
				encoded, marshalErr := proto.Marshal(wire)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				wire = &placementpb.Rules{}
				if unmarshalErr := proto.Unmarshal(encoded, wire); unmarshalErr != nil {
					t.Fatal(unmarshalErr)
				}
			}
			decoded, err := RulesFromProto(wire)
			if err != nil {
				t.Fatal(err)
			}
			before, err := Compile(rules, nil)
			if err != nil {
				t.Fatal(err)
			}
			after, err := Compile(decoded, nil)
			if err != nil {
				t.Fatal(err)
			}
			beforeDigest, _ := Digest(before)
			afterDigest, _ := Digest(after)
			if beforeDigest != afterDigest {
				t.Fatalf("intent changed: %s != %s", beforeDigest, afterDigest)
			}
			if rules != nil && (rules.Preferences == nil) != (decoded.Preferences == nil) {
				t.Fatal("inheritance changed")
			}
			canonical, err := RulesToProto(decoded)
			if err != nil || !proto.Equal(wire, canonical) {
				t.Fatalf("non-idempotent canonical encoding: %v", err)
			}
		})
	}
}

func TestRulesWireRejectsUnsupportedSemantics(t *testing.T) {
	for name, mutate := range map[string]func(*placementpb.Rules){
		"schema":    func(r *placementpb.Rules) { r.SchemaVersion = 2 },
		"order":     func(r *placementpb.Rules) { r.Preferences.Groups[0].Order = 99 },
		"spillover": func(r *placementpb.Rules) { r.Preferences.Groups[0].Spillover = 99 },
		"class":     func(r *placementpb.Rules) { r.Preferences.Groups[0].Match.Classes = []placementpb.ClusterClass{99} },
		"charging": func(r *placementpb.Rules) {
			r.Constraints.Deny = []*placementpb.Selector{{Charging: []placementpb.Charging{0}}}
		},
		"nil selector": func(r *placementpb.Rules) { r.Constraints.Deny = []*placementpb.Selector{nil} },
		"nil group":    func(r *placementpb.Rules) { r.Preferences.Groups = []*placementpb.Group{nil} },
		"unknown root": func(r *placementpb.Rules) {
			r.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1))
		},
		"unknown nested": func(r *placementpb.Rules) {
			r.Preferences.Groups[0].Match.ProtoReflect().SetUnknown(protowire.AppendVarint(protowire.AppendTag(nil, 99, protowire.VarintType), 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			rules := &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{}, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "all", Match: &placementpb.Selector{}}}}}
			mutate(rules)
			if _, err := RulesFromProto(rules); err == nil {
				t.Fatal("unsupported rules accepted")
			}
		})
	}
}

func TestWirePolicySetRevisionAndOverlay(t *testing.T) {
	deny, err := RulesToProto(&Rules{SchemaVersion: 1, Constraints: Constraints{Deny: []Selector{{Classes: []Class{Official}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []*placementpb.PolicySet{{Revision: math.MaxUint64}, {Ingest: deny}, {Serve: deny}} {
		if validationErr := ValidatePolicySet(invalid); validationErr == nil {
			t.Fatal("unversioned/overflowing policy accepted")
		}
	}
	tenant := &placementpb.PolicySet{Revision: 1, Ingest: deny, Serve: deny}
	stream := &placementpb.PolicySet{Revision: 3, Serve: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "all"}}}}}
	p, err := CompilePolicySets(tenant, stream, Serve)
	if err != nil || len(p.Layers) != 2 || len(p.Groups) != 1 || p.Groups[0].ID != "all" {
		t.Fatalf("compile: %+v %v", p, err)
	}
	foundDeny := false
	for _, layer := range p.Layers {
		foundDeny = foundDeny || len(layer.Deny) == 1
	}
	if !foundDeny {
		t.Fatal("overlay erased tenant deny")
	}
	if _, err := CompilePolicySets(nil, nil, "storage"); err == nil {
		t.Fatal("unimplemented verb accepted")
	}
}

func TestVerbUpdatesPreserveSiblingAndClearMeansInheritance(t *testing.T) {
	current := &placementpb.PolicySet{Revision: 12, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{}}}, Serve: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{}}}
	before := proto.CloneOf(current)
	updated, err := ApplyUpdates(current, []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetRevision() != 13 || updated.GetServe() != nil || !proto.Equal(updated.GetIngest(), current.GetIngest()) {
		t.Fatalf("clear changed sibling intent: %+v", updated)
	}
	if !proto.Equal(current, before) {
		t.Fatal("update mutated source snapshot")
	}
	for _, invalid := range [][]*placementpb.VerbUpdate{
		nil,
		{nil},
		{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_SET}},
		{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR, Rules: current.GetServe()}},
		{{Verb: 99, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}},
		{{Verb: placementpb.Verb_VERB_SERVE, Kind: 99}},
		{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}, {Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}},
	} {
		if _, applyErr := ApplyUpdates(current, invalid); applyErr == nil {
			t.Fatalf("invalid updates accepted: %v", invalid)
		}
	}
	current.Revision = math.MaxInt64
	if _, applyErr := ApplyUpdates(current, []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_SERVE, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}}); applyErr == nil {
		t.Fatal("overflowing revision accepted")
	}
}
