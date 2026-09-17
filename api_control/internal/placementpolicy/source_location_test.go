package placementpolicy

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func applied(t *testing.T, own *placementpb.PolicySet, updates []*placementpb.VerbUpdate) *placementpb.PolicySet {
	t.Helper()
	if len(updates) == 0 {
		return own
	}
	next, err := placement.ApplyUpdates(own, updates)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestSourceLocationRoundTripsThroughOwnIngestRules(t *testing.T) {
	location := SourceLocation{Mode: SourceLocationRestricted, Clusters: []SourceLocationCluster{{ClusterID: "edge-b"}, {ClusterID: "edge-a", NodeIDs: []string{"n2", "n1", "n1"}}}, AvoidNodeIDs: []string{"n9"}}
	updates, err := SourceLocationUpdate(nil, location, false)
	if err != nil || len(updates) != 1 {
		t.Fatalf("restricted update: %v %v", updates, err)
	}
	own := applied(t, nil, updates)
	got := SourceLocationOf(own)
	if got.Mode != SourceLocationRestricted || fmt.Sprint(got.Clusters) != "[{edge-a [n1 n2]} {edge-b []}]" || fmt.Sprint(got.AvoidNodeIDs) != "[n9]" {
		t.Fatalf("read back %+v", got)
	}
	if again, againErr := SourceLocationUpdate(own, location, false); againErr != nil || again != nil {
		t.Fatalf("unchanged location produced an update: %v %v", again, againErr)
	}
	cleared, err := SourceLocationUpdate(own, SourceLocation{Mode: SourceLocationAny}, false)
	if err != nil || len(cleared) != 1 || cleared[0].GetKind() != placementpb.UpdateKind_UPDATE_KIND_CLEAR {
		t.Fatalf("ANY without preferences should clear ingest: %v %v", cleared, err)
	}
	if SourceLocationOf(applied(t, own, cleared)).Mode != SourceLocationAny {
		t.Fatal("cleared ingest is not ANY")
	}
}

func TestSourceLocationPreservesPreferencesAndServe(t *testing.T) {
	own := &placementpb.PolicySet{Revision: 4,
		Ingest: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "near"}}}},
		Serve:  &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{Regions: []string{"us"}}}}},
	}
	canonical, err := placement.CanonicalPolicySet(own)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := SourceLocationUpdate(canonical, SourceLocation{Mode: SourceLocationRestricted, Clusters: []SourceLocationCluster{{ClusterID: "edge-a"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	next := applied(t, canonical, updates)
	if next.GetIngest().GetPreferences().GetGroups()[0].GetId() != "near" || !proto.Equal(next.GetServe(), canonical.GetServe()) {
		t.Fatalf("preferences or serve lost: %v", next)
	}
	updates, err = SourceLocationUpdate(next, SourceLocation{Mode: SourceLocationAny}, false)
	if err != nil || len(updates) != 1 || updates[0].GetKind() != placementpb.UpdateKind_UPDATE_KIND_SET {
		t.Fatalf("ANY with preferences must keep them: %v %v", updates, err)
	}
	anyNext := applied(t, next, updates)
	if SourceLocationOf(anyNext).Mode != SourceLocationAny || anyNext.GetIngest().GetPreferences() == nil {
		t.Fatalf("ANY lost preferences: %v", anyNext)
	}
}

func TestSourceLocationCustomRules(t *testing.T) {
	for name, constraints := range map[string]*placementpb.Constraints{
		"region alternative":  {Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{Regions: []string{"eu"}}}}},
		"multi-cluster nodes": {Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"a", "b"}, NodeIds: []string{"n"}}}}},
		"repeated cluster":    {Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"a"}}, {ClusterIds: []string{"a"}, NodeIds: []string{"n"}}}}},
		"deny all":            {Allow: &placementpb.SelectorSet{}},
		"deny only":           {Deny: []*placementpb.Selector{{NodeIds: []string{"n"}}}},
		"class deny":          {Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"a"}}}}, Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}}},
	} {
		t.Run(name, func(t *testing.T) {
			own := &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: constraints}}
			if SourceLocationOf(own).Mode != SourceLocationCustom {
				t.Fatal("custom rules reported as a simple location")
			}
			location := SourceLocation{Mode: SourceLocationRestricted, Clusters: []SourceLocationCluster{{ClusterID: "a"}}}
			if _, err := SourceLocationUpdate(own, location, false); !errors.Is(err, ErrSourceLocationCustom) {
				t.Fatalf("custom rules overwritten without ownership: %v", err)
			}
			updates, err := SourceLocationUpdate(own, location, true)
			if err != nil || SourceLocationOf(applied(t, own, updates)).Mode != SourceLocationRestricted {
				t.Fatalf("declarative owner could not replace custom rules: %v", err)
			}
		})
	}
	// A multi-cluster alternative without nodes is the same restriction.
	multi := &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"b", "a"}}}}}}}
	if got := SourceLocationOf(multi); got.Mode != SourceLocationRestricted || fmt.Sprint(got.ClusterIDs()) != "[a b]" {
		t.Fatalf("multi-cluster alternative: %+v", got)
	}
}

func TestCanonicalSourceLocationRejectsInvalidInput(t *testing.T) {
	for name, location := range map[string]SourceLocation{
		"custom":              {Mode: SourceLocationCustom},
		"unset":               {},
		"any with clusters":   {Mode: SourceLocationAny, Clusters: []SourceLocationCluster{{ClusterID: "a"}}},
		"restricted empty":    {Mode: SourceLocationRestricted},
		"duplicate cluster":   {Mode: SourceLocationRestricted, Clusters: []SourceLocationCluster{{ClusterID: "a"}, {ClusterID: "a"}}},
		"blank node":          {Mode: SourceLocationRestricted, Clusters: []SourceLocationCluster{{ClusterID: "a", NodeIDs: []string{" "}}}},
		"allowed and avoided": {Mode: SourceLocationRestricted, Clusters: []SourceLocationCluster{{ClusterID: "a", NodeIDs: []string{"n"}}}, AvoidNodeIDs: []string{"n"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalSourceLocation(location); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	many := SourceLocation{Mode: SourceLocationRestricted}
	for i := range maxSourceLocationClusters + 1 {
		many.Clusters = append(many.Clusters, SourceLocationCluster{ClusterID: fmt.Sprintf("c%d", i)})
	}
	if _, err := CanonicalSourceLocation(many); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cluster bound not enforced: %v", err)
	}
}

func TestPinsUpdateFreshAllowAndIntersection(t *testing.T) {
	fresh, err := PinsUpdate(nil, []string{"edge-b", "edge-a", "edge-a"})
	if err != nil {
		t.Fatal(err)
	}
	own := applied(t, nil, fresh)
	if got := SourceLocationOf(own); got.Mode != SourceLocationRestricted || fmt.Sprint(got.ClusterIDs()) != "[edge-a edge-b]" {
		t.Fatalf("fresh allow: %+v", got)
	}
	if !PinsExpressed(own, []string{"edge-a", "edge-b"}) {
		t.Fatal("fresh allow does not express the pins")
	}
	if again, againErr := PinsUpdate(own, []string{"edge-a", "edge-b"}); againErr != nil || again != nil {
		t.Fatalf("rerun is not idempotent: %v %v", again, againErr)
	}

	existing := &placementpb.PolicySet{Revision: 2, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
		Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{
			{Regions: []string{"eu"}},
			{ClusterIds: []string{"edge-a", "edge-c"}},
			{ClusterIds: []string{"edge-c"}},
		}},
		Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}}},
	}}}
	canonical, err := placement.CanonicalPolicySet(existing)
	if err != nil {
		t.Fatal(err)
	}
	if PinsExpressed(canonical, []string{"edge-a"}) {
		t.Fatal("unrestricted alternative reported as pinned")
	}
	updates, err := PinsUpdate(canonical, []string{"edge-a"})
	if err != nil {
		t.Fatal(err)
	}
	next := applied(t, canonical, updates)
	alternatives := next.GetIngest().GetConstraints().GetAllow().GetAny()
	if len(alternatives) != 2 || len(next.GetIngest().GetConstraints().GetDeny()) != 1 {
		t.Fatalf("intersection lost rules or kept an empty alternative: %v", next.GetIngest())
	}
	for _, alternative := range alternatives {
		if fmt.Sprint(alternative.GetClusterIds()) != "[edge-a]" {
			t.Fatalf("alternative not restricted to pins: %v", alternative)
		}
	}
	if !PinsExpressed(next, []string{"edge-a"}) {
		t.Fatal("intersection does not express the pins")
	}
	if again, againErr := PinsUpdate(next, []string{"edge-a"}); againErr != nil || again != nil {
		t.Fatalf("intersection rerun is not idempotent: %v %v", again, againErr)
	}

}

func TestPinsUpdateDisjointAllowTakesPins(t *testing.T) {
	disjoint, err := placement.CanonicalPolicySet(&placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1,
		Constraints: &placementpb.Constraints{
			Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"edge-z"}}, {ClusterIds: []string{"edge-y"}, Regions: []string{"eu"}}}},
			Deny:  []*placementpb.Selector{{NodeIds: []string{"node-1"}}},
		},
		Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "near"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := PinsUpdate(disjoint, []string{"edge-b", "edge-a"})
	if err != nil {
		t.Fatal(err)
	}
	next := applied(t, disjoint, updates)
	alternatives := next.GetIngest().GetConstraints().GetAllow().GetAny()
	if len(alternatives) != 2 || fmt.Sprint(alternatives[0].GetClusterIds()) != "[edge-a]" || fmt.Sprint(alternatives[1].GetClusterIds()) != "[edge-b]" {
		t.Fatalf("disjoint allow not replaced by one alternative per pin: %v", next.GetIngest())
	}
	if len(next.GetIngest().GetConstraints().GetDeny()) != 1 || len(next.GetIngest().GetPreferences().GetGroups()) != 1 {
		t.Fatalf("pins replacement dropped deny selectors or preferences: %v", next.GetIngest())
	}
	if !PinsExpressed(next, []string{"edge-a", "edge-b"}) {
		t.Fatal("pins replacement does not express the pins")
	}
	if again, againErr := PinsUpdate(next, []string{"edge-a", "edge-b"}); againErr != nil || again != nil {
		t.Fatalf("pins replacement rerun is not idempotent: %v %v", again, againErr)
	}
}

func TestPinsExpressedRejectsEmptyAllow(t *testing.T) {
	denyAll := &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{}}}}
	if PinsExpressed(denyAll, []string{"edge-a"}) {
		t.Fatal("an empty allow reported as expressing the pins")
	}
	updates, err := PinsUpdate(denyAll, []string{"edge-a"})
	if err != nil {
		t.Fatal(err)
	}
	if next := applied(t, denyAll, updates); fmt.Sprint(SourceLocationOf(next).ClusterIDs()) != "[edge-a]" {
		t.Fatalf("empty allow not replaced by the pins: %v", next.GetIngest())
	}
}
