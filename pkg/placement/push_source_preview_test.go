package placement

import (
	"math"
	"testing"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestPushSourcePreviewWireBindsIdentityAndFreshness(t *testing.T) {
	for _, scenario := range []string{"scope", "node", "generation", "revision", "overflow", "future", "expired", "long lifetime", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			req := &placementpb.PushSourcePreviewQuery{TenantId: "tenant", ControlCellId: "cell", ClusterId: "cluster", InternalName: "stream"}
			out := &placementpb.PushSourcePreviewObservation{Scope: proto.CloneOf(req), NodeId: "node", Generation: "generation", Revision: 1, ObservedAt: timestamppb.New(testNow), ExpiresAt: timestamppb.New(testNow.Add(20 * time.Second))}
			out.Consent = &placementpb.CapacityConsent{AllowIngest: true}
			if err := ValidatePushSourcePreviewObservation(req, out, testNow); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "scope":
				out.Scope.InternalName = "other"
			case "node":
				out.NodeId = ""
			case "generation":
				out.Generation = ""
			case "revision":
				out.Revision = 0
			case "overflow":
				out.Revision = math.MaxUint64
			case "future":
				out.ObservedAt = timestamppb.New(testNow.Add(time.Second))
			case "expired":
				out.ExpiresAt = timestamppb.New(testNow)
			case "long lifetime":
				out.ExpiresAt = timestamppb.New(testNow.Add(time.Minute))
			case "unknown":
				out.ProtoReflect().SetUnknown([]byte{0xa0, 6, 1})
			}
			if ValidatePushSourcePreviewObservation(req, out, testNow) == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
