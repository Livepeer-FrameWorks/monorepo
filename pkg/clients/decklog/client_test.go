package decklog

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestInt64Ptr(t *testing.T) {
	cases := []struct {
		name     string
		input    int64
		expected *int64
	}{
		{name: "negative becomes nil", input: -1, expected: nil},
		{name: "zero becomes nil", input: 0, expected: nil},
		{name: "positive returns pointer", input: 9, expected: int64Pointer(9)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := int64Ptr(tc.input)
			if tc.expected == nil {
				if got != nil {
					t.Fatalf("expected nil, got %#v", got)
				}
				return
			}
			if got == nil || *got != *tc.expected {
				t.Fatalf("expected %d, got %#v", *tc.expected, got)
			}
		})
	}
}

type fakeDecklogServiceClient struct {
	lastTrigger *ipcpb.MistTrigger
}

func (f *fakeDecklogServiceClient) SendEvent(_ context.Context, in *ipcpb.MistTrigger, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.lastTrigger = in
	return &emptypb.Empty{}, nil
}

func (f *fakeDecklogServiceClient) SendServiceEvent(_ context.Context, _ *ipcpb.ServiceEvent, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (f *fakeDecklogServiceClient) SendGatewayTelemetry(_ context.Context, _ *ipcpb.GatewayTelemetryEvent, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func TestSendVodLifecycleCopiesTenantToEnvelope(t *testing.T) {
	fakeClient := &fakeDecklogServiceClient{}
	client := &BatchedClient{
		client: fakeClient,
		logger: logging.NewLogger(),
	}

	tenantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	progress := int32(55)
	err := client.SendVodLifecycle(&ipcpb.VodLifecycleData{
		Status:      ipcpb.VodLifecycleData_STATUS_PROCESSING,
		VodHash:     "vod-1",
		TenantId:    &tenantID,
		ProgressPct: &progress,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fakeClient.lastTrigger == nil {
		t.Fatal("expected SendEvent to receive a trigger")
	}
	if fakeClient.lastTrigger.GetTenantId() != tenantID {
		t.Fatalf("expected envelope tenant %q, got %q", tenantID, fakeClient.lastTrigger.GetTenantId())
	}
	if payload := fakeClient.lastTrigger.GetVodLifecycleData(); payload == nil || payload.GetProgressPct() != progress {
		t.Fatalf("expected progress %d to be preserved, got %#v", progress, payload)
	}
}

func TestSendLifecycleWrappersCopyIdentityToEnvelope(t *testing.T) {
	tenantID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	streamID := "11111111-2222-3333-4444-555555555555"
	nodeID := "edge-node-1"

	tests := []struct {
		name string
		send func(*BatchedClient) error
	}{
		{
			name: "load_balancing",
			send: func(c *BatchedClient) error {
				return c.SendLoadBalancing(&ipcpb.LoadBalancingData{
					SelectedNode:   "https://edge.example/view",
					SelectedNodeId: &nodeID,
					TenantId:       &tenantID,
					StreamId:       &streamID,
				})
			},
		},
		{
			name: "clip_lifecycle",
			send: func(c *BatchedClient) error {
				return c.SendClipLifecycle(&ipcpb.ClipLifecycleData{
					Stage:    ipcpb.ClipLifecycleData_STAGE_DONE,
					ClipHash: "clip-hash",
					TenantId: &tenantID,
					StreamId: &streamID,
					NodeId:   &nodeID,
				})
			},
		},
		{
			name: "dvr_lifecycle",
			send: func(c *BatchedClient) error {
				return c.SendDVRLifecycle(&ipcpb.DVRLifecycleData{
					Status:   ipcpb.DVRLifecycleData_STATUS_STOPPED,
					DvrHash:  "dvr-hash",
					TenantId: &tenantID,
					StreamId: &streamID,
					NodeId:   &nodeID,
				})
			},
		},
		{
			name: "vod_lifecycle",
			send: func(c *BatchedClient) error {
				return c.SendVodLifecycle(&ipcpb.VodLifecycleData{
					Status:   ipcpb.VodLifecycleData_STATUS_COMPLETED,
					VodHash:  "vod-hash",
					TenantId: &tenantID,
					NodeId:   &nodeID,
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := &fakeDecklogServiceClient{}
			client := &BatchedClient{
				client: fakeClient,
				logger: logging.NewLogger(),
			}

			if err := tt.send(client); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fakeClient.lastTrigger == nil {
				t.Fatal("expected SendEvent to receive a trigger")
			}
			if fakeClient.lastTrigger.GetTenantId() != tenantID {
				t.Fatalf("envelope tenant = %q, want %q", fakeClient.lastTrigger.GetTenantId(), tenantID)
			}
			if fakeClient.lastTrigger.GetNodeId() != nodeID {
				t.Fatalf("envelope node = %q, want %q", fakeClient.lastTrigger.GetNodeId(), nodeID)
			}
		})
	}
}

func int64Pointer(v int64) *int64 {
	return &v
}
