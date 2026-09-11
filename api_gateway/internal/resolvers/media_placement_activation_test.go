package resolvers

import (
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestPlacementPolicyActivationEvidence(t *testing.T) {
	fixture := func() *pb.PolicyState {
		return &pb.PolicyState{Scope: &pb.Scope{Kind: pb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}, Own: &pb.PolicySet{Revision: 2}, Inherited: &pb.PolicySet{Revision: 3},
			Active: &pb.PolicySet{Revision: 2}, ActiveParentRevision: 3, Actions: &pb.Actions{}, Features: &pb.Features{SchemaVersion: 1},
			Rollout: &pb.Rollout{Status: pb.RolloutStatus_ROLLOUT_STATUS_EFFECTIVE, RequiredRecipients: 2, AppliedRecipients: 2}}
	}
	for _, scenario := range []string{"matching", "older active pending", "inherited only", "absent active", "future active", "future parent", "wrong parent", "different active policy", "missing recipients", "extra applied", "pending recipient", "tenant has parent", "empty active"} {
		t.Run(scenario, func(t *testing.T) {
			response := fixture()
			valid := false
			switch scenario {
			case "matching":
				valid = true
			case "older active pending":
				response.Rollout.Status = pb.RolloutStatus_ROLLOUT_STATUS_PENDING
				response.Active.Revision, response.ActiveParentRevision = 1, 2
				valid = true
			case "inherited only":
				response.Own, response.Active = &pb.PolicySet{}, &pb.PolicySet{}
				valid = true
			case "absent active":
				response.Active = nil
			case "future active":
				response.Active.Revision++
			case "future parent":
				response.ActiveParentRevision++
			case "wrong parent":
				response.ActiveParentRevision--
			case "different active policy":
				response.Active.Serve = &pb.Rules{SchemaVersion: 1}
			case "missing recipients":
				response.Rollout.AppliedRecipients--
			case "extra applied":
				response.Rollout.AppliedRecipients++
			case "pending recipient":
				response.Rollout.PendingRecipients = []*pb.RolloutRecipient{{Id: "cell", Status: "PENDING"}}
			case "tenant has parent":
				response.Scope = &pb.Scope{Kind: pb.ScopeKind_SCOPE_KIND_TENANT}
			case "empty active":
				response.Own, response.Active, response.Inherited, response.ActiveParentRevision = &pb.PolicySet{}, &pb.PolicySet{}, &pb.PolicySet{}, 0
			}
			before := proto.CloneOf(response)
			out, err := placementPolicyOutput(response)
			if (err == nil) != valid || !proto.Equal(before, response) {
				t.Fatalf("activation validation: out=%+v err=%v valid=%v", out, err, valid)
			}
			if valid && (out.ActiveRevision == nil || out.ActiveParentRevision == nil) {
				t.Fatal("lost explicit activation evidence")
			}
		})
	}
}

func TestPlacementPolicyEmptyActiveMessageRemainsUnconfirmed(t *testing.T) {
	for _, active := range []*pb.PolicySet{nil, {}} {
		response := &pb.PolicyState{Scope: &pb.Scope{Kind: pb.ScopeKind_SCOPE_KIND_STREAM, StreamId: "stream"}, Own: &pb.PolicySet{}, Inherited: &pb.PolicySet{Revision: 3}, Active: active,
			Rollout: &pb.Rollout{Status: pb.RolloutStatus_ROLLOUT_STATUS_NOT_CONFIGURED}, Actions: &pb.Actions{}, Features: &pb.Features{SchemaVersion: 1}}
		out, err := placementPolicyOutput(response)
		if err != nil || out.ActiveRevision != nil || out.ActiveParentRevision != nil || out.ParentRevision != "3" {
			t.Fatalf("invented default activation: %+v %v", out, err)
		}
	}
}
