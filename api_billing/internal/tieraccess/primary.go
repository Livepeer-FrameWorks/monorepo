package tieraccess

import (
	"slices"
	"sort"
)

// EligibleCluster is one platform-official cluster a tenant qualifies for at
// its tier level, with the cluster_pricing tier level that gates it.
type EligibleCluster struct {
	ClusterID     string
	RequiredLevel int32
}

// PrimaryCandidates returns the clusters a tenant's primary may be chosen
// from: the eligible clusters that can be a tenant's primary (Quartermaster
// accepts only media-plane edge clusters) at the highest required tier level
// among them, sorted by cluster ID. Runtime tier reconciliation and the
// desired-state bootstrap both select through here so neither can pick a
// cluster Quartermaster refuses or a different primary than the other.
func PrimaryCandidates(eligible []EligibleCluster, canBePrimary map[string]bool) []string {
	var best int32 = -1
	var out []string
	for _, c := range eligible {
		if !canBePrimary[c.ClusterID] {
			continue
		}
		if c.RequiredLevel > best {
			best = c.RequiredLevel
			out = out[:0]
		}
		if c.RequiredLevel == best && !slices.Contains(out, c.ClusterID) {
			out = append(out, c.ClusterID)
		}
	}
	sort.Strings(out)
	return out
}

// SelectPrimary keeps the tenant's current primary when it is a candidate, so
// a tenant's chosen edge survives reconciliation, and otherwise picks the
// first candidate. It returns "" when there are no candidates.
func SelectPrimary(candidates []string, current string) string {
	if len(candidates) == 0 {
		return ""
	}
	if current != "" && slices.Contains(candidates, current) {
		return current
	}
	return candidates[0]
}
