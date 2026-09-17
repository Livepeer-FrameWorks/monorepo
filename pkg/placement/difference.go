package placement

import (
	"fmt"
	"slices"
	"strings"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

type describedValue struct{ label, value string }

// DescribePolicyChange produces a stable semantic diff, preserving the distinct
// meanings of inheritance, unrestricted rules, and explicit deny-all lists.
func DescribePolicyChange(before, after *placementpb.PolicySet) ([]*placementpb.Difference, error) {
	old, err := describePolicy(before)
	if err != nil {
		return nil, err
	}
	next, err := describePolicy(after)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(old)+len(next))
	for key := range old {
		keys = append(keys, key)
	}
	for key := range next {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	var differences []*placementpb.Difference
	for _, key := range keys {
		previous, existsBefore := old[key]
		current, existsAfter := next[key]
		if existsBefore && existsAfter && previous.value == current.value {
			continue
		}
		label := current.label
		if !existsAfter {
			label = previous.label
			current.value = "Removed"
		}
		if !existsBefore {
			previous.value = "Not configured"
		}
		differences = append(differences, &placementpb.Difference{Path: key, Label: label, Before: previous.value, After: current.value})
	}
	return differences, nil
}

func describePolicy(policy *placementpb.PolicySet) (map[string]describedValue, error) {
	canonical, err := CanonicalPolicySet(policy)
	if err != nil {
		return nil, err
	}
	result := map[string]describedValue{}
	for _, verb := range []Verb{Ingest, Serve} {
		wire := canonical.GetServe()
		label := "Viewership"
		if verb == Ingest {
			wire, label = canonical.GetIngest(), "Ingest"
		}
		prefix := string(verb)
		rules, decodeErr := RulesFromProto(wire)
		if decodeErr != nil {
			return nil, decodeErr
		}
		put := func(path, field, value string) {
			result[prefix+"."+path] = describedValue{label: label + ": " + field, value: value}
		}
		if rules == nil {
			put("mode", "rule source", "Inherit")
			continue
		}
		put("mode", "rule source", "Custom rules")
		allow := "No additional restriction"
		if rules.Constraints.Allow != nil {
			allow = describeSelectors(rules.Constraints.Allow.Any, "No capacity allowed")
		}
		put("constraints.allow", "allowed capacity", allow)
		put("constraints.deny", "denied capacity", describeSelectors(rules.Constraints.Deny, "None"))
		if rules.Preferences == nil {
			put("preferences", "preference order", "Inherit")
			continue
		}
		ids := make([]string, 0, len(rules.Preferences.Groups))
		for _, group := range rules.Preferences.Groups {
			ids = append(ids, group.ID)
		}
		order := "No destination groups (deny all)"
		if len(ids) > 0 {
			order = strings.Join(ids, " → ")
		}
		put("preferences", "preference order", order)
		for _, group := range rules.Preferences.Groups {
			key := "groups." + group.ID + "."
			groupLabel := "group " + group.ID + ", "
			put(key+"match", groupLabel+"capacity", describeSelector(group.Match))
			ordering := "Closest available"
			if group.Order == PriceFirst {
				ordering = "Lowest comparable price (" + group.PriceCurrency + "/" + group.PriceUnit + ")"
			}
			put(key+"order", groupLabel+"selection", ordering)
			spillover := "Never"
			switch group.Spillover {
			case CapacityOnly:
				spillover = "Only when capacity is exhausted"
			case GeoHole:
				spillover = "Only for a geographic hole"
			case CapacityOrGeoHole:
				spillover = "Capacity exhaustion or a geographic hole"
			}
			put(key+"spillover", groupLabel+"fallback", spillover)
			maximum := "Unbounded"
			if group.MaxDistanceKM > 0 {
				maximum = fmt.Sprintf("%g km", group.MaxDistanceKM)
			}
			put(key+"maximumDistance", groupLabel+"maximum distance", maximum)
			if group.GeoHoleDistanceKM > 0 {
				put(key+"geoHoleDistance", groupLabel+"geographic hole threshold", fmt.Sprintf("%g km", group.GeoHoleDistanceKM))
				put(key+"minimumImprovement", groupLabel+"minimum distance improvement", fmt.Sprintf("%g km", group.MinImprovementKM))
			}
		}
	}
	return result, nil
}

func describeSelectors(selectors []Selector, empty string) string {
	if len(selectors) == 0 {
		return empty
	}
	values := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		values = append(values, "("+describeSelector(selector)+")")
	}
	return strings.Join(values, " OR ")
}

func describeSelector(selector Selector) string {
	var parts []string
	if len(selector.ClusterIDs) > 0 {
		parts = append(parts, "clusters: "+strings.Join(selector.ClusterIDs, ", "))
	}
	if len(selector.NodeIDs) > 0 {
		parts = append(parts, "nodes: "+strings.Join(selector.NodeIDs, ", "))
	}
	if len(selector.OwnerIDs) > 0 {
		parts = append(parts, "owners: "+strings.Join(selector.OwnerIDs, ", "))
	}
	if len(selector.Regions) > 0 {
		parts = append(parts, "regions: "+strings.Join(selector.Regions, ", "))
	}
	if len(selector.Classes) > 0 {
		var classes []string
		for _, class := range selector.Classes {
			switch class {
			case Official:
				classes = append(classes, "official")
			case Private:
				classes = append(classes, "my clusters")
			case Marketplace:
				classes = append(classes, "marketplace")
			}
		}
		parts = append(parts, strings.Join(classes, ", "))
	}
	if len(selector.Charging) > 0 {
		var charging []string
		for _, class := range selector.Charging {
			if class == Rated {
				charging = append(charging, "rated")
			} else {
				charging = append(charging, "permanently free")
			}
		}
		parts = append(parts, strings.Join(charging, ", "))
	}
	if len(parts) == 0 {
		return "All entitled capacity"
	}
	return strings.Join(parts, " AND ")
}
