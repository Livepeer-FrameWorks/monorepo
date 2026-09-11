package placementpolicy

import (
	"testing"

	"frameworks/api_control/internal/database/commodoredb"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestSnapshotDoesNotInventActiveDefaultPolicy(t *testing.T) {
	for _, revision := range []int64{0, 1} {
		payload, err := proto.Marshal(&pb.PolicySet{Revision: uint64(revision)})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := decodeSnapshot(Scope{Kind: "tenant"}, commodoredb.CommodoreMediaPlacementPolicy{Revision: revision, PolicyPayload: payload}, commodoredb.CommodoreMediaPlacementPolicy{})
		if err != nil || snapshot.Active != nil || snapshot.ActiveParentRevision != 0 {
			t.Fatalf("revision %d invented activation: %+v %v", revision, snapshot, err)
		}
	}
}

func TestSnapshotRetainsExplicitActiveOwnOrInheritedRevision(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		own, parent := commodoredb.CommodoreMediaPlacementPolicy{}, commodoredb.CommodoreMediaPlacementPolicy{}
		if inherited {
			parent.Revision, own.ActiveParentRevision = 1, 1
		} else {
			own.Revision, own.ActiveRevision = 2, 1
		}
		var err error
		own.PolicyPayload, err = proto.Marshal(&pb.PolicySet{Revision: uint64(own.Revision)})
		if err != nil {
			t.Fatal(err)
		}
		own.ActivePolicyPayload, err = proto.Marshal(&pb.PolicySet{Revision: uint64(own.ActiveRevision)})
		if err != nil {
			t.Fatal(err)
		}
		parent.PolicyPayload, err = proto.Marshal(&pb.PolicySet{Revision: uint64(parent.Revision)})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := decodeSnapshot(Scope{Kind: "stream"}, own, parent)
		if err != nil || snapshot.Active == nil || snapshot.Active.GetRevision() != uint64(own.ActiveRevision) || snapshot.ActiveParentRevision != uint64(own.ActiveParentRevision) {
			t.Fatalf("lost explicit activation: %+v %v", snapshot, err)
		}
	}
}
