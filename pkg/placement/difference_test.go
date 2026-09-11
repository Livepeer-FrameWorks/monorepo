package placement

import (
	"strings"
	"testing"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestDescribePolicyChangePreservesEmptyAndInheritedMeaning(t *testing.T) {
	custom := &placementpb.PolicySet{Revision: 1, Serve: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{}}, Preferences: &placementpb.Preferences{}}}
	differences, err := DescribePolicyChange(nil, custom)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, difference := range differences {
		if strings.HasPrefix(difference.GetPath(), "ingest.") {
			t.Fatal("serve edit changed ingest description")
		}
		values[difference.GetPath()] = difference.GetAfter()
	}
	if values["serve.constraints.allow"] != "No capacity allowed" || values["serve.preferences"] != "No destination groups (deny all)" || values["serve.mode"] != "Custom rules" {
		t.Fatalf("lost empty/inherit distinction: %+v", values)
	}
	clear := &placementpb.PolicySet{Revision: 2}
	differences, err = DescribePolicyChange(custom, clear)
	if err != nil {
		t.Fatal(err)
	}
	modeFound := false
	for _, difference := range differences {
		if difference.GetPath() == "serve.mode" && difference.GetAfter() == "Inherit" {
			modeFound = true
		}
	}
	if !modeFound {
		t.Fatal("clear did not describe restored inheritance")
	}
}

func TestDescribePolicyChangeIgnoresSelectorOrderingButShowsPriority(t *testing.T) {
	before := &placementpb.PolicySet{Revision: 1, Ingest: &placementpb.Rules{SchemaVersion: 1, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{
		{Id: "own", Match: &placementpb.Selector{ClusterIds: []string{"b", "a"}}}, {Id: "fallback"},
	}}}}
	after := proto.CloneOf(before)
	after.Revision++
	after.Ingest.Preferences.Groups[0].Match.ClusterIds = []string{"a", "b"}
	differences, err := DescribePolicyChange(before, after)
	if err != nil || len(differences) != 0 {
		t.Fatalf("selector order generated semantic changes: %+v %v", differences, err)
	}
	after.Ingest.Preferences.Groups[0], after.Ingest.Preferences.Groups[1] = after.Ingest.Preferences.Groups[1], after.Ingest.Preferences.Groups[0]
	differences, err = DescribePolicyChange(before, after)
	if err != nil || len(differences) != 1 || differences[0].GetPath() != "ingest.preferences" || differences[0].GetAfter() != "fallback → own" {
		t.Fatalf("priority change was hidden: %+v %v", differences, err)
	}
}
