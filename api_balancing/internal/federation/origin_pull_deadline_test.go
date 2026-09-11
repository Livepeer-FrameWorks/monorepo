package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

func TestArrangeCanceledWarmPullDoesNotReprepare(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:18009"})
	req := makeReq()
	first, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, canceledErr := deps.ArrangeOriginPull(ctx, req); result != nil || !errors.Is(canceledErr, context.Canceled) || !IsArrangeInfraError(canceledErr) {
		t.Fatalf("cancelled warm lookup returned success or soft refusal: %+v %v", result, canceledErr)
	}
	if len(fed.calls) != 1 {
		t.Fatal("cancelled warm lookup contacted the source again")
	}
	reused, err := deps.ArrangeOriginPull(context.Background(), req)
	if err != nil || !reused.Reused || reused.AttemptID != first.AttemptID || len(fed.calls) != 1 {
		t.Fatalf("warm reuse repeated source setup or lost its physical attempt: %+v %v", reused, err)
	}
}

func TestArrangeLateNotificationCannotPublishPull(t *testing.T) {
	freshRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fed := &fakeNotifyFedClient{mutateAck: func(*federationpb.OriginPullAck) { cancel() }}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:18009"})
	req := makeReq()
	result, err := deps.ArrangeOriginPull(ctx, req)
	if result != nil || !errors.Is(err, context.Canceled) || !IsArrangeInfraError(err) {
		t.Fatalf("late source allow was accepted: %+v %v", result, err)
	}
	if _, exists := control.StreamRegistryInstance.InboundPullForNode(req.InternalName, req.DestNodeID); exists {
		t.Fatal("late source allow published destination state")
	}
}

func TestArrangeExpiredRequestDoesNoWork(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:18009"})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if result, err := deps.ArrangeOriginPull(ctx, makeReq()); result != nil || !errors.Is(err, context.DeadlineExceeded) || !IsArrangeInfraError(err) {
		t.Fatalf("expired request accepted or softened: %+v %v", result, err)
	}
	if len(fed.calls) != 0 {
		t.Fatal("expired request contacted source")
	}
}
