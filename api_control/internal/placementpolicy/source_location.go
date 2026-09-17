package placementpolicy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

// SourceLocationMode classifies a stream's own ingest constraints.
type SourceLocationMode int

const (
	SourceLocationAny SourceLocationMode = iota + 1
	SourceLocationRestricted
	SourceLocationCustom
)

// Bounds follow the selector limits in pkg/placement: one allow alternative per
// cluster, one deny selector for avoided nodes.
const (
	maxSourceLocationClusters = 32
	maxSourceLocationNodes    = 128
)

// ErrSourceLocationCustom reports own ingest rules a source location cannot express.
// Such rules are edited through the media placement review/apply API only.
var ErrSourceLocationCustom = errors.New("stream ingest placement holds rules a source location cannot express")

// SourceLocation is the simple view of a stream's own ingest constraints: any
// cluster, or a list of clusters with optional per-cluster node allow lists and
// avoided nodes. Preference groups are independent of it and are preserved.
type SourceLocation struct {
	Mode         SourceLocationMode
	Clusters     []SourceLocationCluster
	AvoidNodeIDs []string
}

type SourceLocationCluster struct {
	ClusterID string
	// Empty means any node of the cluster.
	NodeIDs []string
}

// ClusterIDs returns the restricted cluster list, nil for ANY.
func (l SourceLocation) ClusterIDs() []string {
	if l.Mode != SourceLocationRestricted {
		return nil
	}
	out := make([]string, 0, len(l.Clusters))
	for _, cluster := range l.Clusters {
		out = append(out, cluster.ClusterID)
	}
	return out
}

// NodeIDs returns every allowed and avoided node ID, sorted and deduplicated.
func (l SourceLocation) NodeIDs() []string {
	var out []string
	for _, cluster := range l.Clusters {
		out = append(out, cluster.NodeIDs...)
	}
	out = append(out, l.AvoidNodeIDs...)
	return sortedUniqueIDs(out)
}

// CanonicalSourceLocation validates write input and returns its canonical form.
// CUSTOM is never accepted as input.
func CanonicalSourceLocation(in SourceLocation) (SourceLocation, error) {
	switch in.Mode {
	case SourceLocationAny:
		if len(in.Clusters) != 0 || len(in.AvoidNodeIDs) != 0 {
			return SourceLocation{}, fmt.Errorf("%w: an unrestricted source location cannot list clusters or nodes", ErrInvalidInput)
		}
		return SourceLocation{Mode: SourceLocationAny}, nil
	case SourceLocationRestricted:
	default:
		return SourceLocation{}, fmt.Errorf("%w: source location must be any or restricted", ErrInvalidInput)
	}
	if len(in.Clusters) == 0 || len(in.Clusters) > maxSourceLocationClusters {
		return SourceLocation{}, fmt.Errorf("%w: a restricted source location needs 1 to %d clusters", ErrInvalidInput, maxSourceLocationClusters)
	}
	out := SourceLocation{Mode: SourceLocationRestricted}
	seenClusters := make(map[string]bool, len(in.Clusters))
	allowed := map[string]bool{}
	for _, cluster := range in.Clusters {
		id := strings.TrimSpace(cluster.ClusterID)
		if !validIdentifier(id, 255) || seenClusters[id] {
			return SourceLocation{}, fmt.Errorf("%w: invalid or duplicate source location cluster %q", ErrInvalidInput, cluster.ClusterID)
		}
		seenClusters[id] = true
		nodes, err := canonicalNodeIDs(cluster.NodeIDs)
		if err != nil {
			return SourceLocation{}, err
		}
		for _, node := range nodes {
			allowed[node] = true
		}
		out.Clusters = append(out.Clusters, SourceLocationCluster{ClusterID: id, NodeIDs: nodes})
	}
	avoid, err := canonicalNodeIDs(in.AvoidNodeIDs)
	if err != nil {
		return SourceLocation{}, err
	}
	for _, node := range avoid {
		if allowed[node] {
			return SourceLocation{}, fmt.Errorf("%w: node %q is both allowed and avoided", ErrInvalidInput, node)
		}
	}
	out.AvoidNodeIDs = avoid
	slices.SortFunc(out.Clusters, func(a, b SourceLocationCluster) int { return strings.Compare(a.ClusterID, b.ClusterID) })
	return out, nil
}

func canonicalNodeIDs(in []string) ([]string, error) {
	if len(in) > maxSourceLocationNodes {
		return nil, fmt.Errorf("%w: too many source location nodes", ErrInvalidInput)
	}
	out := make([]string, 0, len(in))
	for _, node := range in {
		node = strings.TrimSpace(node)
		if !validIdentifier(node, 255) {
			return nil, fmt.Errorf("%w: invalid source location node %q", ErrInvalidInput, node)
		}
		out = append(out, node)
	}
	return sortedUniqueIDs(out), nil
}

func sortedUniqueIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// SourceLocationConstraints compiles a canonical restricted location into ingest
// constraints: one allow alternative per cluster and one deny selector for
// avoided nodes. ANY compiles to no constraints.
func SourceLocationConstraints(location SourceLocation) *placementpb.Constraints {
	if location.Mode != SourceLocationRestricted {
		return nil
	}
	constraints := &placementpb.Constraints{Allow: &placementpb.SelectorSet{}}
	for _, cluster := range location.Clusters {
		constraints.Allow.Any = append(constraints.Allow.Any, &placementpb.Selector{ClusterIds: []string{cluster.ClusterID}, NodeIds: slices.Clone(cluster.NodeIDs)})
	}
	if len(location.AvoidNodeIDs) != 0 {
		constraints.Deny = []*placementpb.Selector{{NodeIds: slices.Clone(location.AvoidNodeIDs)}}
	}
	return constraints
}

// SourceLocationOf reads the source location expressed by a stream's own policy.
// No own ingest constraints is ANY. Allow alternatives naming only clusters (and,
// for a single cluster, nodes) plus deny selectors naming only nodes is
// RESTRICTED. Anything else, including an explicit deny-all, is CUSTOM.
func SourceLocationOf(own *placementpb.PolicySet) SourceLocation {
	constraints := own.GetIngest().GetConstraints()
	if constraints.GetAllow() == nil && len(constraints.GetDeny()) == 0 {
		return SourceLocation{Mode: SourceLocationAny}
	}
	custom := SourceLocation{Mode: SourceLocationCustom}
	if constraints.GetAllow() == nil || len(constraints.GetAllow().GetAny()) == 0 {
		return custom
	}
	location := SourceLocation{Mode: SourceLocationRestricted}
	seen := map[string]bool{}
	for _, selector := range constraints.GetAllow().GetAny() {
		if !onlyClustersAndNodes(selector) || len(selector.GetClusterIds()) == 0 || len(selector.GetNodeIds()) != 0 && len(selector.GetClusterIds()) != 1 {
			return custom
		}
		for _, cluster := range selector.GetClusterIds() {
			if seen[cluster] {
				return custom
			}
			seen[cluster] = true
			location.Clusters = append(location.Clusters, SourceLocationCluster{ClusterID: cluster, NodeIDs: sortedUniqueIDs(selector.GetNodeIds())})
		}
	}
	for _, selector := range constraints.GetDeny() {
		if !onlyClustersAndNodes(selector) || len(selector.GetClusterIds()) != 0 || len(selector.GetNodeIds()) == 0 {
			return custom
		}
		location.AvoidNodeIDs = append(location.AvoidNodeIDs, selector.GetNodeIds()...)
	}
	location.AvoidNodeIDs = sortedUniqueIDs(location.AvoidNodeIDs)
	canonical, err := CanonicalSourceLocation(location)
	if err != nil {
		return custom
	}
	return canonical
}

func onlyClustersAndNodes(selector *placementpb.Selector) bool {
	return len(selector.GetOwnerIds()) == 0 && len(selector.GetRegions()) == 0 && len(selector.GetClasses()) == 0 && len(selector.GetCharging()) == 0
}

// SourceLocationUpdate returns the ingest verb update that makes own express
// location, or nil when own already does. Ingest preferences and the serve verb
// are preserved. CUSTOM own rules are rejected unless replaceCustom is set, which
// only a declarative owner of the stream (bootstrap) may use.
func SourceLocationUpdate(own *placementpb.PolicySet, location SourceLocation, replaceCustom bool) ([]*placementpb.VerbUpdate, error) {
	location, err := CanonicalSourceLocation(location)
	if err != nil {
		return nil, err
	}
	current := SourceLocationOf(own)
	if current.Mode == SourceLocationCustom && !replaceCustom {
		return nil, ErrSourceLocationCustom
	}
	ingest := own.GetIngest()
	preferences := ingest.GetPreferences()
	if location.Mode == SourceLocationAny && preferences == nil {
		if ingest == nil {
			return nil, nil
		}
		return []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_INGEST, Kind: placementpb.UpdateKind_UPDATE_KIND_CLEAR}}, nil
	}
	rules := &placementpb.Rules{SchemaVersion: placement.SchemaVersion, Constraints: SourceLocationConstraints(location), Preferences: proto.CloneOf(preferences)}
	if sameRules(ingest, rules) {
		return nil, nil
	}
	return []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_INGEST, Kind: placementpb.UpdateKind_UPDATE_KIND_SET, Rules: rules}}, nil
}

func sameRules(a, b *placementpb.Rules) bool {
	left, leftErr := canonicalRules(a)
	right, rightErr := canonicalRules(b)
	return leftErr == nil && rightErr == nil && proto.Equal(left, right)
}

func canonicalRules(in *placementpb.Rules) (*placementpb.Rules, error) {
	decoded, err := placement.RulesFromProto(in)
	if err != nil {
		return nil, err
	}
	return placement.RulesToProto(decoded)
}

// PinsUpdate converts a legacy per-source cluster pin into the stream's own
// ingest allow. An existing allow is intersected with the pins, as pins ANDed
// with it: every alternative's clusters are restricted to the pins, alternatives
// left with no cluster are dropped, and an alternative with no clusters takes the
// pins. When own has no allow or no alternative survives, the allow becomes one
// alternative per pinned cluster, so a pin disjoint from the own allow replaces
// it rather than producing an empty allow that denies ingest everywhere. Deny
// selectors and preferences are kept. Returns nil when own already expresses
// the restriction.
func PinsUpdate(own *placementpb.PolicySet, pins []string) ([]*placementpb.VerbUpdate, error) {
	pins = sortedUniqueIDs(pins)
	if len(pins) == 0 {
		return nil, nil
	}
	ingest := own.GetIngest()
	rules := proto.CloneOf(ingest)
	if rules == nil {
		rules = &placementpb.Rules{SchemaVersion: placement.SchemaVersion}
	}
	if rules.Constraints == nil {
		rules.Constraints = &placementpb.Constraints{}
	}
	allow := &placementpb.SelectorSet{}
	for _, selector := range ingest.GetConstraints().GetAllow().GetAny() {
		next := proto.CloneOf(selector)
		if len(next.GetClusterIds()) == 0 {
			next.ClusterIds = slices.Clone(pins)
		} else {
			next.ClusterIds = slices.DeleteFunc(slices.Clone(next.GetClusterIds()), func(cluster string) bool { return !slices.Contains(pins, cluster) })
			if len(next.ClusterIds) == 0 {
				continue
			}
		}
		allow.Any = append(allow.Any, next)
	}
	if len(allow.Any) == 0 {
		for _, pin := range pins {
			allow.Any = append(allow.Any, &placementpb.Selector{ClusterIds: []string{pin}})
		}
	}
	rules.Constraints.Allow = allow
	if sameRules(ingest, rules) {
		return nil, nil
	}
	return []*placementpb.VerbUpdate{{Verb: placementpb.Verb_VERB_INGEST, Kind: placementpb.UpdateKind_UPDATE_KIND_SET, Rules: rules}}, nil
}

// PinsExpressed reports whether own ingest rules restrict ingest to a subset of
// the pinned clusters: an allow with at least one alternative, every alternative
// naming clusters, all of them pinned. An empty allow denies ingest everywhere
// and does not express a pin.
func PinsExpressed(own *placementpb.PolicySet, pins []string) bool {
	pins = sortedUniqueIDs(pins)
	if len(pins) == 0 {
		return true
	}
	allow := own.GetIngest().GetConstraints().GetAllow()
	if len(allow.GetAny()) == 0 {
		return false
	}
	for _, selector := range allow.GetAny() {
		if len(selector.GetClusterIds()) == 0 {
			return false
		}
		for _, cluster := range selector.GetClusterIds() {
			if !slices.Contains(pins, cluster) {
				return false
			}
		}
	}
	return true
}

// PrivateSourceBounded reports whether a compiled ingest policy confines a
// private/multicast pull source to consented clusters: some layer has a
// non-empty allow whose every alternative names only clusters in consented.
// Layers intersect, so one such layer bounds the effective policy.
func PrivateSourceBounded(policy *placement.Policy, consented map[string]bool) bool {
	if policy == nil {
		return false
	}
	for _, layer := range policy.Layers {
		if layer.Allow == nil || len(layer.Allow.Any) == 0 {
			continue
		}
		bounded := true
		for _, selector := range layer.Allow.Any {
			if len(selector.ClusterIDs) == 0 {
				bounded = false
				break
			}
			for _, cluster := range selector.ClusterIDs {
				if !consented[cluster] {
					bounded = false
					break
				}
			}
			if !bounded {
				break
			}
		}
		if bounded {
			return true
		}
	}
	return false
}
