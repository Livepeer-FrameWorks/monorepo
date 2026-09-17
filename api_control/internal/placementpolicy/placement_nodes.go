package placementpolicy

import (
	"slices"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// PlacementNode is a node ID named in placement rules with the clusters its
// selector also names. A selector ANDs its fields, so a node listed beside
// cluster IDs only matches when it belongs to one of them. ClusterIDs is empty
// for nodes that only need ownership: deny selectors (avoided nodes) and
// selectors naming no cluster.
type PlacementNode struct {
	NodeID     string
	ClusterIDs []string
}

// PolicySetPlacementNodes returns every node named by either verb's allow
// alternatives, deny selectors or preference group matches, sorted by node ID
// and deduplicated.
func PolicySetPlacementNodes(set *placementpb.PolicySet) []PlacementNode {
	var out []PlacementNode
	add := func(selector *placementpb.Selector, paired bool) {
		var clusters []string
		if paired {
			clusters = sortedUniqueIDs(selector.GetClusterIds())
		}
		for _, node := range selector.GetNodeIds() {
			out = append(out, PlacementNode{NodeID: node, ClusterIDs: clusters})
		}
	}
	for _, rules := range []*placementpb.Rules{set.GetIngest(), set.GetServe()} {
		for _, selector := range rules.GetConstraints().GetAllow().GetAny() {
			add(selector, true)
		}
		for _, selector := range rules.GetConstraints().GetDeny() {
			add(selector, false)
		}
		for _, group := range rules.GetPreferences().GetGroups() {
			add(group.GetMatch(), true)
		}
	}
	return SortedUniquePlacementNodes(out)
}

// PlacementNodes returns the nodes a restricted source location names: each
// allowed node paired with its cluster, and avoided nodes needing ownership only.
func (l SourceLocation) PlacementNodes() []PlacementNode {
	return PolicySetPlacementNodes(&placementpb.PolicySet{Ingest: &placementpb.Rules{SchemaVersion: placement.SchemaVersion, Constraints: SourceLocationConstraints(l)}})
}

// SortedUniquePlacementNodes returns nodes sorted by node ID then clusters,
// without exact duplicates.
func SortedUniquePlacementNodes(nodes []PlacementNode) []PlacementNode {
	if len(nodes) == 0 {
		return nil
	}
	nodes = slices.Clone(nodes)
	slices.SortFunc(nodes, func(a, b PlacementNode) int {
		if byNode := strings.Compare(a.NodeID, b.NodeID); byNode != 0 {
			return byNode
		}
		return slices.Compare(a.ClusterIDs, b.ClusterIDs)
	})
	return slices.CompactFunc(nodes, func(a, b PlacementNode) bool {
		return a.NodeID == b.NodeID && slices.Equal(a.ClusterIDs, b.ClusterIDs)
	})
}
