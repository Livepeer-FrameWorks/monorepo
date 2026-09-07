package control

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func TestSamePushTargetSetIgnoresEnvelopeRevisionButNotDeliveryPolicy(t *testing.T) {
	current := &ipcpb.ActivatePushTargets{
		TargetRevision:   7,
		SourceGeneration: "generation-a",
		MaxViewers:       3,
		Targets: []*ipcpb.PushTargetSpec{{
			TargetId:  "target-a",
			TargetUri: "rtmps://example.invalid/live/key",
			Name:      "Primary",
			Platform:  "generic",
		}},
	}

	unrelatedRevision := &ipcpb.ActivatePushTargets{
		TargetRevision:   8,
		SourceGeneration: "generation-b",
		MaxViewers:       3,
		Targets: []*ipcpb.PushTargetSpec{{
			TargetId:  "target-a",
			TargetUri: "rtmps://example.invalid/live/key",
			Name:      "Primary",
			Platform:  "generic",
		}},
	}
	if !samePushTargetSet(current, unrelatedRevision) {
		t.Fatal("an envelope-only authority revision must not restart an unchanged output")
	}

	capacityChange := proto.CloneOf(unrelatedRevision)
	capacityChange.MaxViewers = 4
	if samePushTargetSet(current, capacityChange) {
		t.Fatal("a delivery-capacity change must re-arm the output obligation")
	}

	unlimited := proto.CloneOf(unrelatedRevision)
	unlimited.MaxViewers = 0
	if samePushTargetSet(current, unlimited) {
		t.Fatal("a delivery-capacity change to unlimited must re-arm the output obligation")
	}

	targetChange := proto.CloneOf(unrelatedRevision)
	targetChange.Targets[0].TargetUri = "rtmps://example.invalid/live/new-key"
	if samePushTargetSet(current, targetChange) {
		t.Fatal("a target change must re-arm the output obligation")
	}
}
