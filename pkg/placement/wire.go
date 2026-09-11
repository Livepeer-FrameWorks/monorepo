package placement

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protopath"
	"google.golang.org/protobuf/reflect/protorange"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var wireClasses = map[placementpb.ClusterClass]Class{
	placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL:       Official,
	placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE:          Private,
	placementpb.ClusterClass_CLUSTER_CLASS_THIRD_PARTY_MARKETPLACE: Marketplace,
}

var wireCharging = map[placementpb.Charging]Charging{
	placementpb.Charging_CHARGING_RATED:            Rated,
	placementpb.Charging_CHARGING_PERMANENTLY_FREE: PermanentlyFree,
}

var wireOrder = map[placementpb.Order]Order{
	placementpb.Order_ORDER_DISTANCE: DistanceFirst,
	placementpb.Order_ORDER_PRICE:    PriceFirst,
}

var wireSpillover = map[placementpb.Spillover]Spillover{
	placementpb.Spillover_SPILLOVER_NEVER:                Never,
	placementpb.Spillover_SPILLOVER_CAPACITY_ONLY:        CapacityOnly,
	placementpb.Spillover_SPILLOVER_GEO_HOLE:             GeoHole,
	placementpb.Spillover_SPILLOVER_CAPACITY_OR_GEO_HOLE: CapacityOrGeoHole,
}

// RulesFromProto rejects unsupported fields and enum values. It preserves both
// inheritance and explicit empty selectors/preferences across transport boundaries.
func RulesFromProto(in *placementpb.Rules) (*Rules, error) {
	if in == nil {
		return nil, nil
	}
	if err := rejectUnknownWire(in); err != nil {
		return nil, err
	}
	out := &Rules{SchemaVersion: in.GetSchemaVersion()}
	if constraints := in.GetConstraints(); constraints != nil {
		var err error
		out.Constraints.Deny, err = selectorsFromProto(constraints.GetDeny())
		if err != nil {
			return nil, err
		}
		if allow := constraints.GetAllow(); allow != nil {
			selectors, allowErr := selectorsFromProto(allow.GetAny())
			if allowErr != nil {
				return nil, allowErr
			}
			out.Constraints.Allow = &SelectorSet{Any: selectors}
		}
	}
	if preferences := in.GetPreferences(); preferences != nil {
		out.Preferences = &Preferences{}
		for _, group := range preferences.GetGroups() {
			if group == nil {
				return nil, fmt.Errorf("nil placement group")
			}
			selector, err := selectorFromProto(group.GetMatch())
			if err != nil {
				return nil, err
			}
			order, ok := wireOrder[group.GetOrder()]
			if !ok && group.GetOrder() != placementpb.Order_ORDER_UNSPECIFIED {
				return nil, fmt.Errorf("unsupported placement order %d", group.GetOrder())
			}
			spillover, ok := wireSpillover[group.GetSpillover()]
			if !ok && group.GetSpillover() != placementpb.Spillover_SPILLOVER_UNSPECIFIED {
				return nil, fmt.Errorf("unsupported placement spillover %d", group.GetSpillover())
			}
			out.Preferences.Groups = append(out.Preferences.Groups, Group{
				ID: group.GetId(), Match: selector, Order: order, Spillover: spillover,
				MaxDistanceKM: group.GetMaxDistanceKm(), GeoHoleDistanceKM: group.GetGeoHoleDistanceKm(),
				MinImprovementKM: group.GetMinImprovementKm(), PriceCurrency: group.GetPriceCurrency(), PriceUnit: group.GetPriceUnit(),
			})
		}
	}
	if _, err := Compile(out, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// RulesToProto emits a canonical, detached representation suitable for persistence
// and signing. Defaults are explicit, unordered selectors are sorted and deduplicated.
func RulesToProto(in *Rules) (*placementpb.Rules, error) {
	if in == nil {
		return nil, nil
	}
	compiled, err := Compile(in, nil)
	if err != nil {
		return nil, err
	}
	layer := compiled.Layers[0]
	out := &placementpb.Rules{SchemaVersion: SchemaVersion, Constraints: &placementpb.Constraints{Deny: selectorsToProto(layer.Deny)}}
	if layer.Allow != nil {
		out.Constraints.Allow = &placementpb.SelectorSet{Any: selectorsToProto(layer.Allow.Any)}
	}
	if in.Preferences != nil {
		out.Preferences = &placementpb.Preferences{}
		for _, group := range compiled.Groups {
			out.Preferences.Groups = append(out.Preferences.Groups, &placementpb.Group{
				Id: group.ID, Match: selectorToProto(group.Match), Order: enumToProto(group.Order, wireOrder),
				Spillover: enumToProto(group.Spillover, wireSpillover), MaxDistanceKm: group.MaxDistanceKM,
				GeoHoleDistanceKm: group.GeoHoleDistanceKM, MinImprovementKm: group.MinImprovementKM,
				PriceCurrency: group.PriceCurrency, PriceUnit: group.PriceUnit,
			})
		}
	}
	return out, nil
}

// ValidatePolicySet bounds durable revisions and validates even overridden rules.
// A nil set represents the original default; an explicit clear retains its revision.
func ValidatePolicySet(in *placementpb.PolicySet) error {
	if in == nil {
		return nil
	}
	if err := rejectUnknownWire(in); err != nil {
		return err
	}
	if in.GetRevision() > math.MaxInt64 || (in.GetRevision() == 0 && (in.GetIngest() != nil || in.GetServe() != nil)) {
		return fmt.Errorf("invalid placement policy revision")
	}
	for _, rules := range []*placementpb.Rules{in.GetIngest(), in.GetServe()} {
		if _, err := RulesFromProto(rules); err != nil {
			return err
		}
	}
	return nil
}

func CanonicalPolicySet(in *placementpb.PolicySet) (*placementpb.PolicySet, error) {
	if in == nil {
		return nil, nil
	}
	if err := ValidatePolicySet(in); err != nil {
		return nil, err
	}
	out := &placementpb.PolicySet{Revision: in.GetRevision()}
	for _, verb := range []Verb{Ingest, Serve} {
		wire := in.GetIngest()
		if verb == Serve {
			wire = in.GetServe()
		}
		rules, err := RulesFromProto(wire)
		if err != nil {
			return nil, err
		}
		canonical, err := RulesToProto(rules)
		if err != nil {
			return nil, err
		}
		if verb == Ingest {
			out.Ingest = canonical
		} else {
			out.Serve = canonical
		}
	}
	return out, nil
}

// PolicySetsDigest binds effective intent for both verbs. Revisions are separate
// CAS fields; reordering an unordered selector does not change this digest.
func PolicySetsDigest(tenant, stream *placementpb.PolicySet) (string, error) {
	encoded := []byte("frameworks/placement/policy-set/v1\x00")
	for _, verb := range []Verb{Ingest, Serve} {
		policy, err := CompilePolicySets(tenant, stream, verb)
		if err != nil {
			return "", err
		}
		digest, err := Digest(policy)
		if err != nil {
			return "", err
		}
		encoded = append(encoded, []byte(digest)...)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ApplyUpdates edits whole verb sections without dropping an untouched sibling.
// CLEAR removes only that scope's rules; it never means an empty allow list.
func ApplyUpdates(current *placementpb.PolicySet, updates []*placementpb.VerbUpdate) (*placementpb.PolicySet, error) {
	if len(updates) == 0 || len(updates) > 2 {
		return nil, fmt.Errorf("one or two distinct placement verb updates are required")
	}
	result, err := CanonicalPolicySet(current)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &placementpb.PolicySet{}
	}
	if result.GetRevision() == math.MaxInt64 {
		return nil, fmt.Errorf("placement revision exhausted")
	}
	result.Revision++
	seen := make(map[placementpb.Verb]bool)
	for _, update := range updates {
		if update == nil {
			return nil, fmt.Errorf("nil placement update")
		}
		if err := rejectUnknownWire(update); err != nil {
			return nil, err
		}
		verb := update.GetVerb()
		if (verb != placementpb.Verb_VERB_INGEST && verb != placementpb.Verb_VERB_SERVE) || seen[verb] {
			return nil, fmt.Errorf("unknown or duplicate placement verb")
		}
		seen[verb] = true
		var rules *placementpb.Rules
		switch update.GetKind() {
		case placementpb.UpdateKind_UPDATE_KIND_SET:
			if update.GetRules() == nil {
				return nil, fmt.Errorf("SET requires placement rules")
			}
			rules = proto.CloneOf(update.GetRules())
		case placementpb.UpdateKind_UPDATE_KIND_CLEAR:
			if update.GetRules() != nil {
				return nil, fmt.Errorf("CLEAR cannot carry placement rules")
			}
		default:
			return nil, fmt.Errorf("unknown placement update kind")
		}
		if verb == placementpb.Verb_VERB_INGEST {
			result.Ingest = rules
		} else {
			result.Serve = rules
		}
	}
	return CanonicalPolicySet(result)
}

func CompilePolicySets(tenant, stream *placementpb.PolicySet, verb Verb) (*Policy, error) {
	if verb != Ingest && verb != Serve {
		return nil, fmt.Errorf("unsupported placement verb %q", verb)
	}
	for _, set := range []*placementpb.PolicySet{tenant, stream} {
		if err := ValidatePolicySet(set); err != nil {
			return nil, err
		}
	}
	selectRules := func(set *placementpb.PolicySet) *placementpb.Rules {
		if verb == Ingest {
			return set.GetIngest()
		}
		return set.GetServe()
	}
	tenantRules, err := RulesFromProto(selectRules(tenant))
	if err != nil {
		return nil, err
	}
	streamRules, err := RulesFromProto(selectRules(stream))
	if err != nil {
		return nil, err
	}
	return Compile(tenantRules, streamRules)
}

func selectorsFromProto(in []*placementpb.Selector) ([]Selector, error) {
	var out []Selector
	for _, value := range in {
		if value == nil {
			return nil, fmt.Errorf("nil placement selector")
		}
		selector, err := selectorFromProto(value)
		if err != nil {
			return nil, err
		}
		out = append(out, selector)
	}
	return out, nil
}

func selectorFromProto(in *placementpb.Selector) (Selector, error) {
	out := Selector{ClusterIDs: slices.Clone(in.GetClusterIds()), OwnerIDs: slices.Clone(in.GetOwnerIds()), Regions: slices.Clone(in.GetRegions())}
	for _, value := range in.GetClasses() {
		class, ok := wireClasses[value]
		if !ok {
			return Selector{}, fmt.Errorf("unsupported placement class %d", value)
		}
		out.Classes = append(out.Classes, class)
	}
	for _, value := range in.GetCharging() {
		charging, ok := wireCharging[value]
		if !ok {
			return Selector{}, fmt.Errorf("unsupported charging class %d", value)
		}
		out.Charging = append(out.Charging, charging)
	}
	return out, nil
}

func selectorsToProto(in []Selector) []*placementpb.Selector {
	var out []*placementpb.Selector
	for _, selector := range in {
		out = append(out, selectorToProto(selector))
	}
	return out
}

func selectorToProto(in Selector) *placementpb.Selector {
	out := &placementpb.Selector{ClusterIds: slices.Clone(in.ClusterIDs), OwnerIds: slices.Clone(in.OwnerIDs), Regions: slices.Clone(in.Regions)}
	for _, value := range in.Classes {
		out.Classes = append(out.Classes, enumToProto(value, wireClasses))
	}
	for _, value := range in.Charging {
		out.Charging = append(out.Charging, enumToProto(value, wireCharging))
	}
	return out
}

func enumToProto[P ~int32, T ~string](value T, values map[P]T) P {
	for key, known := range values {
		if value == known {
			return key
		}
	}
	return 0
}

func rejectUnknownWire(in proto.Message) error {
	return protorange.Range(in.ProtoReflect(), func(values protopath.Values) error {
		if message, ok := values.Index(-1).Value.Interface().(protoreflect.Message); ok && len(message.GetUnknown()) != 0 {
			return fmt.Errorf("unsupported placement fields at %s", values.Path)
		}
		return nil
	})
}
