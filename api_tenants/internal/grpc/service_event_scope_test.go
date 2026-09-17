package grpc

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestServiceEventScopeRequiresTenantOutsidePlatformEvents(t *testing.T) {
	cases := []struct {
		event   *ipcpb.ServiceEvent
		scope   string
		wantErr bool
	}{
		{event: &ipcpb.ServiceEvent{EventType: eventClusterCreated, TenantId: "tenant-1"}, scope: "tenant"},
		{event: &ipcpb.ServiceEvent{EventType: eventClusterCreated}, scope: "platform"},
		{event: &ipcpb.ServiceEvent{EventType: eventClusterUpdated}, scope: "platform"},
		{event: &ipcpb.ServiceEvent{EventType: eventClusterInviteCreated}, wantErr: true},
		{event: &ipcpb.ServiceEvent{EventType: eventTenantCreated}, wantErr: true},
	}
	for _, tc := range cases {
		scope, err := serviceEventScope(tc.event)
		if (err != nil) != tc.wantErr || scope != tc.scope {
			t.Fatalf("serviceEventScope(%s, tenant=%q) = %q, %v; want %q, error %v",
				tc.event.GetEventType(), tc.event.GetTenantId(), scope, err, tc.scope, tc.wantErr)
		}
	}
}
