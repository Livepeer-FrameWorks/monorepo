package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
)

// --- helpers ---

func buildRelay(t *testing.T, store *state.RedisStateStore, instanceID, addr string, pool CommandRelayPool) *CommandRelay {
	t.Helper()
	return &CommandRelay{
		store:         store,
		instanceID:    instanceID,
		advertiseAddr: addr,
		pool:          pool,
		logger:        logging.NewLogger(),
	}
}

func setCommandRelay(t *testing.T, r *CommandRelay) {
	t.Helper()
	prev := commandRelay
	commandRelay = r
	t.Cleanup(func() { commandRelay = prev })
}

func ensureRegistry(t *testing.T) {
	t.Helper()
	prev := registry
	registry = &Registry{conns: make(map[string]*conn), log: logging.NewLogger()}
	t.Cleanup(func() { registry = prev })
}

func newTestStore(t *testing.T) (*state.RedisStateStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	return state.NewRedisStateStore(client, "test-cluster"), mr
}

// --- mocks ---

type mockRelayClient struct {
	relay pb.FoghornRelayClient
}

func (m *mockRelayClient) Relay() pb.FoghornRelayClient { return m.relay }

type mockRelayPool struct {
	client CommandRelayClient
	err    error
}

func (m *mockRelayPool) GetOrCreate(_, _ string) (CommandRelayClient, error) {
	return m.client, m.err
}

type fakeFoghornRelayClient struct {
	resp *pb.ForwardCommandResponse
	err  error
}

func (f *fakeFoghornRelayClient) ForwardCommand(_ context.Context, _ *pb.ForwardCommandRequest, _ ...grpc.CallOption) (*pb.ForwardCommandResponse, error) {
	return f.resp, f.err
}

type fakeControlStream struct {
	pb.HelmsmanControl_ConnectServer
	sent []*pb.ControlMessage
}

func (f *fakeControlStream) Send(msg *pb.ControlMessage) error {
	f.sent = append(f.sent, msg)
	return nil
}

type trackingRelayPool struct {
	called *bool
}

func (p *trackingRelayPool) GetOrCreate(_, _ string) (CommandRelayClient, error) {
	*p.called = true
	return nil, fmt.Errorf("should not be called")
}

// --- ConnOwner round-trip ---

func TestConnOwnerEncodeDecode(t *testing.T) {
	store, mr := newTestStore(t)
	ctx := context.Background()

	t.Run("round trip", func(t *testing.T) {
		if err := store.SetConnOwner(ctx, "node-1", "inst-abc", "10.0.0.1:9090"); err != nil {
			t.Fatalf("SetConnOwner: %v", err)
		}
		owner, err := store.GetConnOwner(ctx, "node-1")
		if err != nil {
			t.Fatalf("GetConnOwner: %v", err)
		}
		if owner.InstanceID != "inst-abc" {
			t.Fatalf("expected InstanceID=inst-abc, got %q", owner.InstanceID)
		}
		if owner.GRPCAddr != "10.0.0.1:9090" {
			t.Fatalf("expected GRPCAddr=10.0.0.1:9090, got %q", owner.GRPCAddr)
		}
	})

	t.Run("no separator backwards compat", func(t *testing.T) {
		key := "{test-cluster}:conn_owner:node-legacy"
		mr.Set(key, "legacy-instance-only")

		owner, err := store.GetConnOwner(ctx, "node-legacy")
		if err != nil {
			t.Fatalf("GetConnOwner: %v", err)
		}
		if owner.InstanceID != "legacy-instance-only" {
			t.Fatalf("expected InstanceID=legacy-instance-only, got %q", owner.InstanceID)
		}
		if owner.GRPCAddr != "" {
			t.Fatalf("expected empty GRPCAddr for legacy value, got %q", owner.GRPCAddr)
		}
	})

	t.Run("missing key returns empty", func(t *testing.T) {
		owner, err := store.GetConnOwner(ctx, "node-nonexistent")
		if err != nil {
			t.Fatalf("GetConnOwner: %v", err)
		}
		if owner.InstanceID != "" || owner.GRPCAddr != "" {
			t.Fatalf("expected zero ConnOwner for missing key, got %+v", owner)
		}
	})
}

// --- forward() ---

func TestForward_NoOwner(t *testing.T) {
	store, _ := newTestStore(t)
	r := buildRelay(t, store, "self-1", "10.0.0.1:9090", nil)

	err := r.forward(context.Background(), &pb.ForwardCommandRequest{
		TargetNodeId: "unknown-node",
	})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("expected ErrNotConnected, got %v", err)
	}
}

func TestForward_OwnerIsSelf(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.SetConnOwner(ctx, "node-1", "self-instance", "10.0.0.1:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", nil)

	err := r.forward(ctx, &pb.ForwardCommandRequest{TargetNodeId: "node-1"})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("expected ErrNotConnected when owner is self, got %v", err)
	}
}

func TestForward_OwnerNoAddr(t *testing.T) {
	store, mr := newTestStore(t)
	ctx := context.Background()

	key := "{test-cluster}:conn_owner:node-1"
	mr.Set(key, "other-instance")

	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", nil)

	err := r.forward(ctx, &pb.ForwardCommandRequest{TargetNodeId: "node-1"})
	if err == nil {
		t.Fatal("expected error when owner has no address")
	}
	if !strings.Contains(err.Error(), "no address") {
		t.Fatalf("expected 'no address' in error, got %v", err)
	}
}

func TestForward_Success(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.SetConnOwner(ctx, "node-1", "peer-instance", "10.0.0.2:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	fakeRelay := &fakeFoghornRelayClient{
		resp: &pb.ForwardCommandResponse{Delivered: true},
	}
	pool := &mockRelayPool{
		client: &mockRelayClient{relay: fakeRelay},
	}

	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", pool)

	err := r.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: "node-1",
		Command:      &pb.ForwardCommandRequest_DvrStop{DvrStop: &pb.DVRStopRequest{}},
	})
	if err != nil {
		t.Fatalf("expected nil error on successful forward, got %v", err)
	}
}

func TestForward_PeerRejects(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.SetConnOwner(ctx, "node-1", "peer-instance", "10.0.0.2:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	fakeRelay := &fakeFoghornRelayClient{
		resp: &pb.ForwardCommandResponse{Delivered: false, Error: "node disconnected"},
	}
	pool := &mockRelayPool{
		client: &mockRelayClient{relay: fakeRelay},
	}

	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", pool)

	err := r.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: "node-1",
		Command:      &pb.ForwardCommandRequest_ClipPull{ClipPull: &pb.ClipPullRequest{}},
	})
	if err == nil {
		t.Fatal("expected error when peer rejects command")
	}
	if !strings.Contains(err.Error(), "node disconnected") {
		t.Fatalf("expected rejection reason in error, got %v", err)
	}
}

func TestForward_DialError(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.SetConnOwner(ctx, "node-1", "peer-instance", "10.0.0.2:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	pool := &mockRelayPool{err: fmt.Errorf("connection refused")}
	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", pool)

	err := r.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: "node-1",
		Command:      &pb.ForwardCommandRequest_DvrStop{DvrStop: &pb.DVRStopRequest{}},
	})
	if err == nil {
		t.Fatal("expected error on dial failure")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected dial error, got %v", err)
	}
}

// --- Send* with relay ---

func TestSendWithRelay_LocalSuccess(t *testing.T) {
	ensureRegistry(t)

	fakeStream := &fakeControlStream{}
	registry.mu.Lock()
	registry.conns["node-1"] = &conn{stream: fakeStream}
	registry.mu.Unlock()

	store, _ := newTestStore(t)
	poolCalled := false
	pool := &trackingRelayPool{called: &poolCalled}

	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", pool)
	setCommandRelay(t, r)

	err := SendStopSessions("node-1", &pb.StopSessionsRequest{})
	if err != nil {
		t.Fatalf("expected nil error when local send succeeds, got %v", err)
	}
	if poolCalled {
		t.Fatal("relay pool should not be called when local send succeeds")
	}
}

func TestSendWithRelay_LocalFailRelay(t *testing.T) {
	ensureRegistry(t)

	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.SetConnOwner(ctx, "node-1", "peer-instance", "10.0.0.2:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	fakeRelay := &fakeFoghornRelayClient{
		resp: &pb.ForwardCommandResponse{Delivered: true},
	}
	pool := &mockRelayPool{
		client: &mockRelayClient{relay: fakeRelay},
	}

	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", pool)
	setCommandRelay(t, r)

	err := SendClipPull("node-1", &pb.ClipPullRequest{})
	if err != nil {
		t.Fatalf("expected nil error after relay success, got %v", err)
	}
}

func TestSendWithRelay_NoRelay(t *testing.T) {
	ensureRegistry(t)
	setCommandRelay(t, nil)

	err := SendDVRStop("node-1", &pb.DVRStopRequest{})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("expected ErrNotConnected when relay is nil, got %v", err)
	}
}

func TestSendWithRelay_MultipleSendTypes(t *testing.T) {
	ensureRegistry(t)

	store, _ := newTestStore(t)
	ctx := context.Background()
	if err := store.SetConnOwner(ctx, "node-1", "peer-instance", "10.0.0.2:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	fakeRelay := &fakeFoghornRelayClient{
		resp: &pb.ForwardCommandResponse{Delivered: true},
	}
	pool := &mockRelayPool{
		client: &mockRelayClient{relay: fakeRelay},
	}
	r := buildRelay(t, store, "self-instance", "10.0.0.1:9090", pool)
	setCommandRelay(t, r)

	tests := []struct {
		name string
		fn   func() error
	}{
		{"DVRStart", func() error { return SendDVRStart("node-1", &pb.DVRStartRequest{}) }},
		{"DVRStop", func() error { return SendDVRStop("node-1", &pb.DVRStopRequest{}) }},
		{"ClipDelete", func() error { return SendClipDelete("node-1", &pb.ClipDeleteRequest{}) }},
		{"DVRDelete", func() error { return SendDVRDelete("node-1", &pb.DVRDeleteRequest{}) }},
		{"VodDelete", func() error { return SendVodDelete("node-1", &pb.VodDeleteRequest{}) }},
		{"DefrostRequest", func() error { return SendDefrostRequest("node-1", &pb.DefrostRequest{}) }},
		{"DtshSyncRequest", func() error { return SendDtshSyncRequest("node-1", &pb.DtshSyncRequest{}) }},
		{"StopSessions", func() error { return SendStopSessions("node-1", &pb.StopSessionsRequest{}) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err != nil {
				t.Fatalf("Send%s relay should succeed, got %v", tc.name, err)
			}
		})
	}
}
