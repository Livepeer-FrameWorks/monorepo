package grpc

import (
	"context"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestSendServiceEventAcceptsPlatformScopedClusterEventWithoutTenant(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	event := &ipcpb.ServiceEvent{
		EventId:      "event-platform-cluster",
		EventType:    "cluster_created",
		Source:       "quartermaster",
		ResourceType: "cluster",
		ResourceId:   "edge-eu",
		Payload:      &ipcpb.ServiceEvent_ClusterEvent{ClusterEvent: &ipcpb.ClusterEvent{ClusterId: "edge-eu"}},
	}
	if _, err := server.SendServiceEvent(context.Background(), event); err != nil {
		t.Fatalf("platform-scoped cluster event rejected: %v", err)
	}
	if len(producer.produceCalls) != 1 {
		t.Fatalf("expected 1 kafka message, got %d", len(producer.produceCalls))
	}
}

func TestSendServiceEventRejectsTenantlessTenantScopedClusterEvent(t *testing.T) {
	producer := &fakeProducer{}
	server := newTestServer(producer)

	event := &ipcpb.ServiceEvent{
		EventId:   "event-invite",
		EventType: "cluster_invite_created",
		Source:    "quartermaster",
		Payload:   &ipcpb.ServiceEvent_ClusterEvent{ClusterEvent: &ipcpb.ClusterEvent{ClusterId: "edge-eu"}},
	}
	if _, err := server.SendServiceEvent(context.Background(), event); err == nil {
		t.Fatal("expected tenantless cluster_invite_created to be rejected")
	}
	if len(producer.produceCalls) != 0 {
		t.Fatalf("expected no kafka messages, got %d", len(producer.produceCalls))
	}
}
