package placement

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func nodeCandidate(cluster, node string) Candidate {
	return Candidate{TenantID: "tenant", ClusterID: cluster, NodeID: node, OwnerTenantID: "tenant", AllowedVerbs: []Verb{Ingest, Serve}}
}

func nodeRequest(policy *Policy) Request {
	return Request{TenantID: "tenant", Verb: Ingest, Now: time.Unix(1_700_000_000, 0), Policy: policy}
}

func TestNodeSelectorMatchesCandidateNode(t *testing.T) {
	allow := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Allow: &SelectorSet{Any: []Selector{
		{ClusterIDs: []string{"eu"}, NodeIDs: []string{"eu-1"}},
		{ClusterIDs: []string{"us"}},
	}}}}, Groups: []Group{{ID: "all"}}}
	for name, tc := range map[string]struct {
		candidate Candidate
		want      Reason
	}{
		"listed node":               {nodeCandidate("eu", "eu-1"), Eligible},
		"unlisted node":             {nodeCandidate("eu", "eu-2"), PolicyDenied},
		"cluster without node list": {nodeCandidate("us", "us-9"), Eligible},
		"unlisted cluster":          {nodeCandidate("ap", "eu-1"), PolicyDenied},
		"cluster-only candidate":    {nodeCandidate("eu", ""), PolicyFactsUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			reason, err := CheckConstraints(nodeRequest(allow), tc.candidate)
			if err != nil || reason != tc.want {
				t.Fatalf("got %s %v, want %s", reason, err, tc.want)
			}
		})
	}

	deny := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{{NodeIDs: []string{"eu-2"}}}}}, Groups: []Group{{ID: "all"}}}
	if reason, _ := CheckConstraints(nodeRequest(deny), nodeCandidate("eu", "eu-2")); reason != PolicyDenied {
		t.Fatalf("avoided node admitted: %s", reason)
	}
	if reason, _ := CheckConstraints(nodeRequest(deny), nodeCandidate("eu", "eu-1")); reason != Eligible {
		t.Fatalf("other node refused: %s", reason)
	}
	// A cluster-level check cannot prove the eventual node escapes the deny.
	if reason, _ := CheckConstraints(nodeRequest(deny), nodeCandidate("eu", "")); reason != PolicyFactsUnavailable {
		t.Fatalf("cluster-only candidate escaped node deny: %s", reason)
	}
}

func TestNodeSelectorValidationLimits(t *testing.T) {
	values := make([]string, maxSelectorValues+1)
	for i := range values {
		values[i] = "node-" + strings.Repeat("x", i%8) + string(rune('a'+i%26))
	}
	for name, selector := range map[string]Selector{
		"too many nodes": {NodeIDs: values},
		"empty node":     {NodeIDs: []string{""}},
		"padded node":    {NodeIDs: []string{" node"}},
		"oversized node": {NodeIDs: []string{strings.Repeat("n", 256)}},
	} {
		t.Run(name, func(t *testing.T) {
			policy := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Deny: []Selector{selector}}}}
			if err := Validate(policy); err == nil {
				t.Fatal("invalid node selector accepted")
			}
		})
	}
}

func TestNodeSelectorCanonicalDigest(t *testing.T) {
	a := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Allow: &SelectorSet{Any: []Selector{{ClusterIDs: []string{"eu"}, NodeIDs: []string{"b", "a", "a"}}}}}}}
	b := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Allow: &SelectorSet{Any: []Selector{{ClusterIDs: []string{"eu"}, NodeIDs: []string{"a", "b"}}}}}}}
	da, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(b)
	if err != nil || da != db {
		t.Fatalf("node order changed digest: %s %s %v", da, db, err)
	}
	b.Layers[0].Allow.Any[0].NodeIDs = []string{"a"}
	if db, _ = Digest(b); da == db {
		t.Fatal("node set missing from digest")
	}
	withoutNodes := &Policy{SchemaVersion: SchemaVersion, Layers: []Constraints{{Allow: &SelectorSet{Any: []Selector{{ClusterIDs: []string{"eu"}}}}}}}
	if dn, _ := Digest(withoutNodes); dn == da {
		t.Fatal("node restriction indistinguishable from cluster restriction")
	}
}

// Policies without node selectors must encode exactly as before the field
// existed; signed authorities and stored receipts bind those digests.
func TestSelectorEncodingWithoutNodesIsUnchanged(t *testing.T) {
	encoded, err := json.Marshal(Selector{ClusterIDs: []string{"eu"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"ClusterIDs":["eu"],"OwnerIDs":null,"Regions":null,"Classes":null,"Charging":null}` {
		t.Fatalf("selector encoding changed: %s", encoded)
	}
	wire, err := RulesToProto(&Rules{SchemaVersion: SchemaVersion, Constraints: Constraints{Deny: []Selector{{ClusterIDs: []string{"eu"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "node") || proto.Size(wire.GetConstraints().GetDeny()[0]) != len("eu")+2 {
		t.Fatalf("selector wire encoding changed: %x", payload)
	}
}

func TestNodeSelectorWireRoundTripAndHelpers(t *testing.T) {
	set := &placementpb.PolicySet{Revision: 3,
		Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{
			Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"eu"}, NodeIds: []string{"eu-2", "eu-1"}}}},
			Deny:  []*placementpb.Selector{{NodeIds: []string{"eu-9"}}},
		}},
		Serve: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "near", Match: &placementpb.Selector{NodeIds: []string{"us-1"}}}}}},
	}
	canonical, err := CanonicalPolicySet(set)
	if err != nil {
		t.Fatal(err)
	}
	if got := canonical.GetIngest().GetConstraints().GetAllow().GetAny()[0].GetNodeIds(); len(got) != 2 || got[0] != "eu-1" {
		t.Fatalf("node IDs not canonical: %v", got)
	}
	encoded, err := proto.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	decoded := &placementpb.PolicySet{}
	if err = proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatal(err)
	}
	again, err := CanonicalPolicySet(decoded)
	if err != nil || !proto.Equal(again, canonical) {
		t.Fatalf("node selectors lost in round trip: %v", err)
	}
	if !PolicySetHasNodeSelectors(set) {
		t.Fatal("node selectors not detected")
	}
	if ids := PolicySetNodeIDs(set); strings.Join(ids, ",") != "eu-1,eu-2,eu-9,us-1" {
		t.Fatalf("node IDs: %v", ids)
	}
	set.Ingest, set.Serve = nil, nil
	if PolicySetHasNodeSelectors(set) || PolicySetNodeIDs(nil) != nil {
		t.Fatal("empty set reported node selectors")
	}
	differences, err := DescribePolicyChange(nil, canonical)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, difference := range differences {
		found = found || strings.Contains(difference.GetAfter(), "nodes: eu-1, eu-2")
	}
	if !found {
		t.Fatalf("difference omits node selector: %v", differences)
	}
}
