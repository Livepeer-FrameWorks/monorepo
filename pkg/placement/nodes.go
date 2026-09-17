package placement

import (
	"slices"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// PolicySetNodeIDs returns every node ID named by either verb's constraints or
// preference groups, sorted and deduplicated. Authorities carrying any of them
// require media authority placement schema 3.
func PolicySetNodeIDs(set *placementpb.PolicySet) []string {
	var out []string
	for _, rules := range []*placementpb.Rules{set.GetIngest(), set.GetServe()} {
		out = append(out, rulesNodeIDs(rules)...)
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// PolicySetHasNodeSelectors reports whether any selector in the set names nodes.
func PolicySetHasNodeSelectors(set *placementpb.PolicySet) bool {
	for _, rules := range []*placementpb.Rules{set.GetIngest(), set.GetServe()} {
		if len(rulesNodeIDs(rules)) != 0 {
			return true
		}
	}
	return false
}

func rulesNodeIDs(rules *placementpb.Rules) []string {
	if rules == nil {
		return nil
	}
	var out []string
	for _, selector := range rules.GetConstraints().GetAllow().GetAny() {
		out = append(out, selector.GetNodeIds()...)
	}
	for _, selector := range rules.GetConstraints().GetDeny() {
		out = append(out, selector.GetNodeIds()...)
	}
	for _, group := range rules.GetPreferences().GetGroups() {
		out = append(out, group.GetMatch().GetNodeIds()...)
	}
	return out
}
