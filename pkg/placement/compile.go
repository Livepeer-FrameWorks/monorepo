package placement

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// Preferences distinguishes inheritance (nil) from an explicit empty group list (deny all).
type Preferences struct {
	Groups []Group
}

// Rules is one verb's intent at a tenant or stream scope. Hard constraints always accumulate;
// preferences replace the inherited list as a whole when explicitly supplied.
type Rules struct {
	SchemaVersion uint32
	Constraints   Constraints
	Preferences   *Preferences
}

// Compile copies and intersects tenant/stream intent without mutating either input. Candidate
// entitlement and capacity-owner consent remain required facts, not rights granted by this policy.
func Compile(tenant, stream *Rules) (*Policy, error) {
	if tenant == nil && stream == nil {
		return nil, nil
	}
	p := &Policy{SchemaVersion: SchemaVersion, Groups: []Group{{ID: "entitled", Spillover: Never, Order: DistanceFirst}}}
	for _, rules := range []*Rules{tenant, stream} {
		if rules == nil {
			continue
		}
		if rules.SchemaVersion != SchemaVersion {
			return nil, fmt.Errorf("unsupported rule schema %d", rules.SchemaVersion)
		}
		own := &Policy{SchemaVersion: rules.SchemaVersion, Layers: []Constraints{rules.Constraints}}
		if rules.Preferences != nil {
			own.Groups = rules.Preferences.Groups
		}
		if err := Validate(own); err != nil {
			return nil, fmt.Errorf("invalid scoped rules: %w", err)
		}
		p.Layers = append(p.Layers, rules.Constraints)
		if rules.Preferences != nil {
			p.Groups = rules.Preferences.Groups
		}
	}
	if err := Validate(p); err != nil {
		return nil, err
	}
	return canonicalCopy(p), nil
}

// Digest binds canonical effective rules, including hard constraints and group order. Nil policy
// retains its own identity so absence cannot be confused with an explicit policy on replay.
func Digest(p *Policy) (string, error) {
	if err := Validate(p); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canonicalCopy(p))
	if err != nil {
		return "", fmt.Errorf("encode placement policy: %w", err)
	}
	sum := sha256.Sum256(append([]byte("frameworks/placement/v1\x00"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

func canonicalCopy(p *Policy) *Policy {
	if p == nil {
		return nil
	}
	result := &Policy{SchemaVersion: p.SchemaVersion}
	for _, layer := range p.Layers {
		copy := Constraints{Deny: canonicalSelectors(layer.Deny)}
		if layer.Allow != nil {
			copy.Allow = &SelectorSet{Any: canonicalSelectors(layer.Allow.Any)}
		}
		result.Layers = append(result.Layers, copy)
	}
	// Layers and OR selectors carry no priority. Normalizing them prevents order-only changes
	// from changing signed policy digests; preference groups retain their original order.
	slices.SortFunc(result.Layers, compareConstraints)
	result.Layers = slices.CompactFunc(result.Layers, func(a, b Constraints) bool { return compareConstraints(a, b) == 0 })
	for _, group := range p.Groups {
		group.Match = canonicalSelector(group.Match)
		if group.Spillover == "" {
			group.Spillover = Never
		}
		if group.Order == "" {
			group.Order = DistanceFirst
		}
		if group.MaxDistanceKM == 0 {
			group.MaxDistanceKM = 0
		}
		if group.GeoHoleDistanceKM == 0 {
			group.GeoHoleDistanceKM = 0
		}
		if group.MinImprovementKM == 0 {
			group.MinImprovementKM = 0
		}
		result.Groups = append(result.Groups, group)
	}
	return result
}

func compareConstraints(a, b Constraints) int {
	if a.Allow == nil && b.Allow != nil {
		return -1
	}
	if a.Allow != nil && b.Allow == nil {
		return 1
	}
	if a.Allow != nil {
		if n := slices.CompareFunc(a.Allow.Any, b.Allow.Any, compareSelectors); n != 0 {
			return n
		}
	}
	return slices.CompareFunc(a.Deny, b.Deny, compareSelectors)
}

func compareSelectors(a, b Selector) int {
	for _, pair := range [][2][]string{{a.ClusterIDs, b.ClusterIDs}, {a.OwnerIDs, b.OwnerIDs}, {a.Regions, b.Regions}} {
		if n := slices.Compare(pair[0], pair[1]); n != 0 {
			return n
		}
	}
	if n := slices.Compare(a.Classes, b.Classes); n != 0 {
		return n
	}
	// Node IDs compare last so ordering among selectors without nodes is unchanged.
	if n := slices.Compare(a.Charging, b.Charging); n != 0 {
		return n
	}
	return slices.Compare(a.NodeIDs, b.NodeIDs)
}

func canonicalSelectors(values []Selector) []Selector {
	var result []Selector
	for _, value := range values {
		result = append(result, canonicalSelector(value))
	}
	slices.SortFunc(result, compareSelectors)
	return slices.CompactFunc(result, func(a, b Selector) bool { return compareSelectors(a, b) == 0 })
}

func canonicalSelector(s Selector) Selector {
	s.ClusterIDs = sortedUnique(s.ClusterIDs)
	s.OwnerIDs = sortedUnique(s.OwnerIDs)
	s.Regions = sortedUnique(s.Regions)
	s.Classes = sortedUnique(s.Classes)
	s.Charging = sortedUnique(s.Charging)
	s.NodeIDs = sortedUnique(s.NodeIDs)
	return s
}

func sortedUnique[T ~string](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	result := slices.Clone(values)
	slices.Sort(result)
	return slices.Compact(result)
}
