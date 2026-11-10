package control

import (
	"context"
	"testing"

	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestStopRelayEffectFailurePreservesConnectedOwner(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if ok, err := store.AcquireConnOwnerFenced(ctx, "stop-node", "peer", "peer:9090", 7); err != nil || !ok {
		t.Fatalf("seed owner: ok=%v err=%v", ok, err)
	}
	peer := &fakeFoghornRelayClient{resp: &foghornrelaypb.ForwardCommandResponse{Delivered: false, Error: "publisher generation changed"}}
	relay := buildRelay(t, store, "local", "local:9090", &mockRelayPool{client: &mockRelayClient{relay: peer}})
	err := relay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "stop-node", Command: &foghornrelaypb.ForwardCommandRequest_StopSessions{StopSessions: &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}}})
	if err == nil {
		t.Fatal("unconfirmed stop unexpectedly succeeded")
	}
	owner, readErr := store.GetConnOwner(ctx, "stop-node")
	if readErr != nil || owner.InstanceID != "peer" || owner.Fence != 7 {
		t.Fatalf("effect failure erased a healthy node owner: owner=%+v err=%v", owner, readErr)
	}
}

func TestStopRelayLegacyDeliveryCannotConfirmEffect(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if ok, err := store.AcquireConnOwnerFenced(ctx, "stop-node", "old-peer", "peer:9090", 7); err != nil || !ok {
		t.Fatalf("seed owner: ok=%v err=%v", ok, err)
	}
	peer := &fakeFoghornRelayClient{resp: &foghornrelaypb.ForwardCommandResponse{Delivered: true}}
	relay := buildRelay(t, store, "local", "local:9090", &mockRelayPool{client: &mockRelayClient{relay: peer}})
	err := relay.forward(ctx, &foghornrelaypb.ForwardCommandRequest{TargetNodeId: "stop-node", Command: &foghornrelaypb.ForwardCommandRequest_StopSessions{StopSessions: &ipcpb.StopSessionsRequest{TenantId: "tenant", StreamNames: []string{"stream"}}}})
	if err == nil {
		t.Fatal("legacy Send-only Delivered=true falsely completed a confirmed stop")
	}
	owner, readErr := store.GetConnOwner(ctx, "stop-node")
	if readErr != nil || owner.InstanceID != "old-peer" {
		t.Fatalf("legacy peer capability failure erased its node owner: owner=%+v err=%v", owner, readErr)
	}
}
