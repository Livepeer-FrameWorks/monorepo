package decklog

import (
	"context"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

type capturingClient struct {
	mu            sync.Mutex
	lastTrigger   *ipcpb.MistTrigger
	lastService   *ipcpb.ServiceEvent
	lastCtx       context.Context
	serviceCalled chan struct{}
}

func newCapturingClient() *capturingClient {
	return &capturingClient{serviceCalled: make(chan struct{}, 4)}
}

func (f *capturingClient) SendEvent(ctx context.Context, in *ipcpb.MistTrigger, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.lastTrigger = in
	f.lastCtx = ctx
	f.mu.Unlock()
	return &emptypb.Empty{}, nil
}

func (f *capturingClient) SendServiceEvent(_ context.Context, in *ipcpb.ServiceEvent, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	f.mu.Lock()
	f.lastService = in
	f.mu.Unlock()
	f.serviceCalled <- struct{}{}
	return &emptypb.Empty{}, nil
}

func (f *capturingClient) SendGatewayTelemetry(_ context.Context, _ *ipcpb.GatewayTelemetryEvent, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func newTestClient(fake *capturingClient) *BatchedClient {
	return &BatchedClient{client: fake, logger: logging.NewLogger(), source: "foghorn"}
}

func TestNewEventIDIsValidUUIDv7(t *testing.T) {
	id := newEventID()
	parsed, err := uuid.Parse(id)
	if err != nil {
		t.Fatalf("newEventID produced unparseable UUID %q: %v", id, err)
	}
	if parsed.Version() != 7 {
		t.Fatalf("expected UUIDv7, got version %d", parsed.Version())
	}
}

func TestAuthContextFromAttachesToken(t *testing.T) {
	c := &BatchedClient{serviceToken: "tok-1"}
	ctx := c.authContextFrom(context.Background())
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		t.Fatal("expected outgoing metadata")
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer tok-1" {
		t.Fatalf("authorization = %v, want [Bearer tok-1]", got)
	}
}

func TestAuthContextFromNoTokenLeavesMetadataEmpty(t *testing.T) {
	c := &BatchedClient{}
	ctx := c.authContextFrom(context.Background())
	if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get("authorization")) != 0 {
		t.Fatalf("expected no authorization metadata, got %v", md.Get("authorization"))
	}
}

func TestAuthContextFromNilContextDefaultsToBackground(t *testing.T) {
	c := &BatchedClient{serviceToken: "tok-2"}
	//nolint:staticcheck // Testing nil context handling
	ctx := c.authContextFrom(nil)
	if ctx == nil {
		t.Fatal("expected non-nil context")
	}
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok || len(md.Get("authorization")) != 1 {
		t.Fatalf("expected authorization on nil-derived context, got ok=%v md=%v", ok, md)
	}
}

func TestStampTriggerEnvelope(t *testing.T) {
	c := &BatchedClient{clusterID: "cluster-A", sourceRegion: "eu-west"}

	t.Run("nil trigger is a no-op", func(t *testing.T) {
		c.stampTriggerEnvelope(nil)
	})

	t.Run("fills empty fields", func(t *testing.T) {
		trigger := &ipcpb.MistTrigger{}
		c.stampTriggerEnvelope(trigger)
		if trigger.GetEventId() == "" {
			t.Fatal("event_id must be stamped when empty")
		}
		if trigger.GetSchemaVersion() != envelopeSchemaVersion {
			t.Fatalf("schema_version = %d, want %d", trigger.GetSchemaVersion(), envelopeSchemaVersion)
		}
		if trigger.GetClusterId() != "cluster-A" {
			t.Fatalf("cluster_id = %q, want cluster-A", trigger.GetClusterId())
		}
		if trigger.GetSourceRegion() != "eu-west" {
			t.Fatalf("source_region = %q, want eu-west", trigger.GetSourceRegion())
		}
	})

	t.Run("preserves caller-set fields", func(t *testing.T) {
		existingCluster := "caller-cluster"
		trigger := &ipcpb.MistTrigger{
			EventId:       "caller-event",
			SchemaVersion: 99,
			ClusterId:     &existingCluster,
			SourceRegion:  "caller-region",
		}
		c.stampTriggerEnvelope(trigger)
		if trigger.GetEventId() != "caller-event" {
			t.Fatalf("event_id overwritten: %q", trigger.GetEventId())
		}
		if trigger.GetSchemaVersion() != 99 {
			t.Fatalf("schema_version overwritten: %d", trigger.GetSchemaVersion())
		}
		if trigger.GetClusterId() != "caller-cluster" {
			t.Fatalf("cluster_id overwritten: %q", trigger.GetClusterId())
		}
		if trigger.GetSourceRegion() != "caller-region" {
			t.Fatalf("source_region overwritten: %q", trigger.GetSourceRegion())
		}
	})
}

func TestStampTriggerEnvelopeNoStampWhenConfigEmpty(t *testing.T) {
	c := &BatchedClient{}
	trigger := &ipcpb.MistTrigger{}
	c.stampTriggerEnvelope(trigger)
	if trigger.GetClusterId() != "" {
		t.Fatalf("cluster_id stamped from empty config: %q", trigger.GetClusterId())
	}
	if trigger.GetSourceRegion() != "" {
		t.Fatalf("source_region stamped from empty config: %q", trigger.GetSourceRegion())
	}
}

func TestStampServiceEnvelope(t *testing.T) {
	c := &BatchedClient{clusterID: "cluster-B", sourceRegion: "us-east"}

	t.Run("nil event is a no-op", func(t *testing.T) {
		c.stampServiceEnvelope(nil)
	})

	t.Run("fills empty fields", func(t *testing.T) {
		event := &ipcpb.ServiceEvent{}
		c.stampServiceEnvelope(event)
		if event.GetEventId() == "" {
			t.Fatal("event_id must be stamped when empty")
		}
		if event.GetSchemaVersion() != envelopeSchemaVersion {
			t.Fatalf("schema_version = %d, want %d", event.GetSchemaVersion(), envelopeSchemaVersion)
		}
		if event.GetSourceClusterId() != "cluster-B" {
			t.Fatalf("source_cluster_id = %q, want cluster-B", event.GetSourceClusterId())
		}
		if event.GetSourceRegion() != "us-east" {
			t.Fatalf("source_region = %q, want us-east", event.GetSourceRegion())
		}
	})

	t.Run("preserves caller-set fields", func(t *testing.T) {
		event := &ipcpb.ServiceEvent{
			EventId:         "ev",
			SchemaVersion:   42,
			SourceClusterId: "caller",
			SourceRegion:    "caller-region",
		}
		c.stampServiceEnvelope(event)
		if event.GetEventId() != "ev" || event.GetSchemaVersion() != 42 || event.GetSourceClusterId() != "caller" || event.GetSourceRegion() != "caller-region" {
			t.Fatalf("caller fields overwritten: %#v", event)
		}
	})
}

func TestStampGatewayEnvelope(t *testing.T) {
	c := &BatchedClient{}

	c.stampGatewayEnvelope(nil)

	event := &ipcpb.GatewayTelemetryEvent{}
	c.stampGatewayEnvelope(event)
	if event.GetEventId() == "" {
		t.Fatal("event_id must be stamped")
	}
	if event.GetSchemaVersion() != envelopeSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", event.GetSchemaVersion(), envelopeSchemaVersion)
	}

	existing := &ipcpb.GatewayTelemetryEvent{EventId: "keep", SchemaVersion: 7}
	c.stampGatewayEnvelope(existing)
	if existing.GetEventId() != "keep" || existing.GetSchemaVersion() != 7 {
		t.Fatalf("caller fields overwritten: %#v", existing)
	}
}

func TestSendLoadBalancingOptionalFields(t *testing.T) {
	streamID := "stream-1"
	tenantID := "tenant-1"
	nodeID := "node-1"

	tests := []struct {
		name       string
		data       *ipcpb.LoadBalancingData
		wantStream string
		wantTenant string
		wantNode   string
	}{
		{
			name:       "all set",
			data:       &ipcpb.LoadBalancingData{StreamId: &streamID, TenantId: &tenantID, SelectedNodeId: &nodeID},
			wantStream: "stream-1",
			wantTenant: "tenant-1",
			wantNode:   "node-1",
		},
		{
			name:       "all empty",
			data:       &ipcpb.LoadBalancingData{},
			wantStream: "",
			wantTenant: "",
			wantNode:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newCapturingClient()
			c := newTestClient(fake)
			if err := c.SendLoadBalancing(tt.data); err != nil {
				t.Fatalf("SendLoadBalancing: %v", err)
			}
			if fake.lastTrigger.GetStreamId() != tt.wantStream {
				t.Fatalf("stream_id = %q, want %q", fake.lastTrigger.GetStreamId(), tt.wantStream)
			}
			if fake.lastTrigger.GetTenantId() != tt.wantTenant {
				t.Fatalf("tenant_id = %q, want %q", fake.lastTrigger.GetTenantId(), tt.wantTenant)
			}
			if fake.lastTrigger.GetNodeId() != tt.wantNode {
				t.Fatalf("node_id = %q, want %q", fake.lastTrigger.GetNodeId(), tt.wantNode)
			}
		})
	}
}

func TestSendClipLifecycleStreamIDOptional(t *testing.T) {
	tenantID := "tenant-1"
	streamID := "stream-1"

	tests := []struct {
		name       string
		streamID   *string
		wantStream string
	}{
		{name: "stream set", streamID: &streamID, wantStream: "stream-1"},
		{name: "stream empty", streamID: nil, wantStream: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newCapturingClient()
			c := newTestClient(fake)
			err := c.SendClipLifecycle(&ipcpb.ClipLifecycleData{
				Stage:    ipcpb.ClipLifecycleData_STAGE_DONE,
				ClipHash: "clip-1",
				TenantId: &tenantID,
				StreamId: tt.streamID,
			})
			if err != nil {
				t.Fatalf("SendClipLifecycle: %v", err)
			}
			if fake.lastTrigger.GetStreamId() != tt.wantStream {
				t.Fatalf("stream_id = %q, want %q", fake.lastTrigger.GetStreamId(), tt.wantStream)
			}
		})
	}
}

func TestSendDVRLifecycleStreamIDOptional(t *testing.T) {
	tenantID := "tenant-1"
	streamID := "stream-1"

	tests := []struct {
		name       string
		streamID   *string
		wantStream string
	}{
		{name: "stream set", streamID: &streamID, wantStream: "stream-1"},
		{name: "stream empty", streamID: nil, wantStream: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newCapturingClient()
			c := newTestClient(fake)
			err := c.SendDVRLifecycle(&ipcpb.DVRLifecycleData{
				Status:   ipcpb.DVRLifecycleData_STATUS_STOPPED,
				DvrHash:  "dvr-1",
				TenantId: &tenantID,
				StreamId: tt.streamID,
			})
			if err != nil {
				t.Fatalf("SendDVRLifecycle: %v", err)
			}
			if fake.lastTrigger.GetStreamId() != tt.wantStream {
				t.Fatalf("stream_id = %q, want %q", fake.lastTrigger.GetStreamId(), tt.wantStream)
			}
		})
	}
}

func TestEmitArtifactLifecycleEventGuards(t *testing.T) {
	t.Run("nil client", func(t *testing.T) {
		var c *BatchedClient
		c.emitArtifactLifecycleEvent(&ipcpb.ServiceEvent{})
	})
	t.Run("nil event", func(t *testing.T) {
		c := newTestClient(newCapturingClient())
		c.emitArtifactLifecycleEvent(nil)
	})
	t.Run("nil grpc client", func(t *testing.T) {
		c := &BatchedClient{logger: logging.NewLogger()}
		c.emitArtifactLifecycleEvent(&ipcpb.ServiceEvent{EventType: "x"})
	})
}

func TestEmitArtifactLifecycleEventFillsSourceAndTimestamp(t *testing.T) {
	fake := newCapturingClient()
	c := newTestClient(fake)
	c.emitArtifactLifecycleEvent(&ipcpb.ServiceEvent{EventType: "artifact_lifecycle", TenantId: "tenant-1"})

	<-fake.serviceCalled
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastService == nil {
		t.Fatal("expected service event to be sent")
	}
	if fake.lastService.GetSource() != "foghorn" {
		t.Fatalf("source = %q, want foghorn (filled from client)", fake.lastService.GetSource())
	}
	if fake.lastService.GetTimestamp() == nil {
		t.Fatal("timestamp must be filled when nil")
	}
}

func TestEmitArtifactLifecycleEventPreservesSource(t *testing.T) {
	fake := newCapturingClient()
	c := newTestClient(fake)
	c.emitArtifactLifecycleEvent(&ipcpb.ServiceEvent{EventType: "artifact_lifecycle", Source: "caller-source", TenantId: "tenant-1"})

	<-fake.serviceCalled
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastService.GetSource() != "caller-source" {
		t.Fatalf("source overwritten: %q, want caller-source", fake.lastService.GetSource())
	}
}
