package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	"google.golang.org/protobuf/proto"
)

func TestArrangeBindsPlacementReceiptBeforeNotifyAndPreservesWarmAttempt(t *testing.T) {
	freshRegistry(t)
	store, _, prepare := placementReceiptFixture(t)
	ctx := context.Background()
	if _, err := store.Begin(ctx, prepare); err != nil {
		t.Fatal(err)
	}
	fed := &fakeNotifyFedClient{mutateAck: func(ack *federationpb.OriginPullAck) {
		receipt, err := store.Begin(ctx, prepare)
		if err != nil || receipt.Pull == nil || receipt.Pull.AttemptID != ack.AttemptId || receipt.Response != nil {
			t.Fatalf("source notified before durable physical binding: %+v, %v", receipt, err)
		}
	}}
	deps := makeDeps(t, fed, map[string]string{"eu-cell": "peer:18009"})
	req := makeReq()
	req.InternalName, req.TenantID, req.DestClusterID, req.DestNodeID = prepare.Query.InternalName, prepare.Query.TenantId, prepare.ClusterId, prepare.NodeId
	req.RemoteCluster = "eu-cell"
	req.Remote.ClusterId = "eu"
	req.Remote.SourceGeneration, req.Remote.SourceRevision = prepare.Query.SourceGeneration, 7
	req.DestinationFence = 9007199254740993
	req.BindPull = func(pull *PlacementPullBinding) error { return store.BindPull(ctx, prepare, pull) }
	first, err := deps.ArrangeOriginPull(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	next := proto.CloneOf(prepare)
	next.AttemptId, err = placement.NewPreparationAttemptID(store.now().Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Begin(ctx, next); err != nil {
		t.Fatal(err)
	}
	req.BindPull = func(pull *PlacementPullBinding) error { return store.BindPull(ctx, next, pull) }
	reused, err := deps.ArrangeOriginPull(ctx, req)
	if err != nil || !reused.Reused || reused.AttemptID != first.AttemptID || len(fed.calls) != 1 {
		t.Fatalf("new viewer repeated or relabeled source setup: %+v, %v", reused, err)
	}
	receipt, err := store.Begin(ctx, next)
	if err != nil || receipt.Pull == nil || receipt.Pull.AttemptID != first.AttemptID || receipt.Pull.SourceGeneration != next.Query.SourceGeneration || receipt.Pull.DestinationFence != req.DestinationFence {
		t.Fatalf("warm viewer receipt lost physical binding: %+v, %v", receipt, err)
	}
}

func TestArrangeBindingFailureCannotNotifyOrReuse(t *testing.T) {
	for _, warm := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			t.Run(map[bool]string{false: "cold", true: "warm"}[warm]+"/"+map[bool]string{false: "refused", true: "canceled"}[canceled], func(t *testing.T) {
				registry := freshRegistry(t)
				fed := &fakeNotifyFedClient{}
				deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:18009"})
				req := makeReq()
				req.Remote.ClusterId = "virtual-source"
				if warm {
					if _, err := deps.ArrangeOriginPull(context.Background(), req); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				refused := errors.New("receipt binding refused")
				req.DestinationFence = 9007199254740993
				req.BindPull = func(*PlacementPullBinding) error {
					if canceled {
						cancel()
						return nil
					}
					return refused
				}
				calls := len(fed.calls)
				result, err := deps.ArrangeOriginPull(ctx, req)
				want := refused
				if canceled {
					want = context.Canceled
				}
				if result != nil || !errors.Is(err, want) || !IsArrangeInfraError(err) || len(fed.calls) != calls {
					t.Fatalf("binding refusal escaped to source: %+v, %v, calls=%d", result, err, len(fed.calls))
				}
				if _, found := registry.InboundPullForNode(req.InternalName, req.DestNodeID); found != warm {
					t.Fatal("failed binding published or destroyed a physical pull")
				}
			})
		}
	}
}
