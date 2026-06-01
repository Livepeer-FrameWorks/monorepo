package federation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type fakeNotifyFedClient struct {
	mu    sync.Mutex
	calls []*pb.OriginPullNotification
	acks  []*pb.OriginPullAck
	errs  []error
}

func (f *fakeNotifyFedClient) NotifyOriginPull(_ context.Context, _, _ string, req *pb.OriginPullNotification) (*pb.OriginPullAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := len(f.calls)
	f.calls = append(f.calls, req)
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.acks) {
		return f.acks[idx], nil
	}
	return &pb.OriginPullAck{Accepted: true, DtscUrl: "dtsc://peer/" + req.StreamName}, nil
}

type fakePeerResolver struct{ addrs map[string]string }

func (f *fakePeerResolver) GetPeerAddr(clusterID string) string { return f.addrs[clusterID] }

// freshRegistry installs a temporary StreamRegistry for the duration of the
// test and restores the prior global on cleanup.
func freshRegistry(t *testing.T) *control.StreamRegistry {
	t.Helper()
	prev := control.StreamRegistryInstance
	r := control.NewStreamRegistry(nil, "cluster-local", 0)
	control.SetStreamRegistry(r)
	t.Cleanup(func() { control.SetStreamRegistry(prev) })
	return r
}

func makeDeps(t *testing.T, fed *fakeNotifyFedClient, addrs map[string]string) *ArrangeOriginPullDeps {
	t.Helper()
	cache, _ := setupTestCache(t)
	return &ArrangeOriginPullDeps{
		Cache:        cache,
		PeerResolver: &fakePeerResolver{addrs: addrs},
		FedClient:    fed,
		InstanceID:   "foghorn-test",
		ClusterID:    "cluster-local",
		Logger:       testLogger(),
	}
}

func makeReq() ArrangeOriginPullRequest {
	return ArrangeOriginPullRequest{
		InternalName:    "stream-1",
		Remote:          &pb.EdgeCandidate{NodeId: "remote-node", BaseUrl: "https://peer"},
		RemoteCluster:   "cluster-peer",
		TenantID:        "tenant-1",
		DestNodeID:      "local-edge",
		DestNodeBaseURL: "local-edge.cluster-local",
	}
}

func TestArrange_DepsMissing_Returns_ErrOriginPullDepsMissing(t *testing.T) {
	freshRegistry(t)
	d := &ArrangeOriginPullDeps{Logger: testLogger()}
	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullDepsMissing) {
		t.Fatalf("want ErrOriginPullDepsMissing, got %v", err)
	}
}

func TestArrange_InvalidRequest_Refused(t *testing.T) {
	freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})
	if _, err := d.ArrangeOriginPull(context.Background(), ArrangeOriginPullRequest{}); err == nil {
		t.Fatal("expected invalid-request error")
	}
}

func TestArrange_RegistryNil_Refused(t *testing.T) {
	// No freshRegistry — leave global nil to assert the fail-closed guard.
	prev := control.StreamRegistryInstance
	control.SetStreamRegistry(nil)
	t.Cleanup(func() { control.SetStreamRegistry(prev) })

	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})
	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullRegistryNil) {
		t.Fatalf("want ErrOriginPullRegistryNil, got %v", err)
	}
}

func TestArrange_HappyPath_MarksReplicating(t *testing.T) {
	r := freshRegistry(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})

	res, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if res == nil || res.PullDTSCURL == "" {
		t.Fatalf("expected non-empty DTSC URL, got %+v", res)
	}
	loc, ok := r.LocalReplication(context.Background(), "stream-1")
	if !ok {
		t.Fatal("expected MarkReplicating to seed registry")
	}
	if loc.ReplicatingFrom != "cluster-peer" || loc.DestNodeID != "local-edge" {
		t.Fatalf("unexpected location: %+v", loc)
	}
	instances := sm.GetStreamInstances("stream-1")
	inst, ok := instances["local-edge"]
	if !ok {
		t.Fatal("expected local-edge stream instance")
	}
	if inst.Inputs != 1 || !inst.Replicated {
		t.Fatalf("stream instance = %+v, want inputs=1 replicated=true", inst)
	}
	if len(fed.calls) != 1 || fed.calls[0].StreamName != "stream-1" {
		t.Fatalf("NotifyOriginPull calls = %+v", fed.calls)
	}
}

func TestArrange_PeerUnreachable_Refused(t *testing.T) {
	freshRegistry(t)
	// PeerResolver has no entry for cluster-peer.
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{})
	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullPeerUnreachable) {
		t.Fatalf("want ErrOriginPullPeerUnreachable, got %v", err)
	}
}

func TestArrange_NotifyRejected_Refused(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{
		acks: []*pb.OriginPullAck{{Accepted: false, Reason: "peer refused"}},
	}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullNotifyFailed) {
		t.Fatalf("want ErrOriginPullNotifyFailed, got %v", err)
	}
}

func TestArrange_NotifyRPCError_Refused(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{
		errs: []error{errors.New("conn refused")},
	}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullNotifyFailed) {
		t.Fatalf("want ErrOriginPullNotifyFailed, got %v", err)
	}
}

func TestArrange_NoDest_Refused(t *testing.T) {
	freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.DestNodeID = ""
	req.LBPicker = nil // mutually exclusive — both empty triggers the refuse
	_, err := d.ArrangeOriginPull(context.Background(), req)
	if !errors.Is(err, ErrOriginPullNoDest) {
		t.Fatalf("want ErrOriginPullNoDest, got %v", err)
	}
}

func TestArrange_LBPickerError_PropagatedAsWrappedError(t *testing.T) {
	freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.DestNodeID = ""
	req.LBPicker = func(context.Context, float64, float64, string) (string, string, error) {
		return "", "", errors.New("no edges")
	}
	_, err := d.ArrangeOriginPull(context.Background(), req)
	if err == nil {
		t.Fatal("expected LB error")
	}
	if errors.Is(err, ErrOriginPullNoDest) {
		t.Fatalf("LB error should not be classified as NoDest, got %v", err)
	}
}

func TestArrange_LoopPreventionRejected(t *testing.T) {
	freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})

	// Seed cache with an existing remote replication FROM cluster-peer
	// (i.e. they are already pulling this stream FROM us). Trying to
	// pull from them in reverse must be refused.
	if err := d.Cache.SetRemoteReplication(context.Background(), "cluster-peer", &RemoteReplicationEntry{
		StreamName: "stream-1",
		ClusterID:  "cluster-peer",
		NodeID:     "their-edge",
		Available:  true,
	}); err != nil {
		t.Fatalf("seed remote replication: %v", err)
	}

	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullLoop) {
		t.Fatalf("want ErrOriginPullLoop, got %v", err)
	}
}

func TestArrange_LockContention_ReturnsReusedOnRace(t *testing.T) {
	r := freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})

	// Pre-seed registry as if a prior arrangement already completed for
	// this stream. Reuse fast-path should hit before lock contention is
	// even attempted.
	r.MarkReplicating("stream-1", "cluster-peer", "dtsc://peer/stream-1", "local-edge", "local-edge.cluster-local", "remote-node")

	res, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if err != nil {
		t.Fatalf("expected reuse to short-circuit, got %v", err)
	}
	if res == nil || !res.Reused {
		t.Fatalf("expected Reused=true, got %+v", res)
	}
}

func TestArrange_LockContention_HeldByOther_Refused(t *testing.T) {
	freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})

	// Another foghorn instance holds the lock; ours must lose.
	if !d.Cache.TryAcquireOriginPullLock(context.Background(), "stream-1", "other-foghorn") {
		t.Fatal("seed lock should succeed")
	}
	t.Cleanup(func() {
		d.Cache.ReleaseOriginPullLock(context.Background(), "stream-1", "other-foghorn")
	})

	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullLockContention) {
		t.Fatalf("want ErrOriginPullLockContention, got %v", err)
	}
}

func TestIsArrangeInfraError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"deps missing", ErrOriginPullDepsMissing, true},
		{"registry nil", ErrOriginPullRegistryNil, true},
		{"peer unreachable", ErrOriginPullPeerUnreachable, true},
		{"notify failed", ErrOriginPullNotifyFailed, true},
		{"wrapped notify failed", errors.Join(errors.New("ctx"), ErrOriginPullNotifyFailed), true},
		{"no dest (soft)", ErrOriginPullNoDest, false},
		{"lock contention (soft)", ErrOriginPullLockContention, false},
		{"loop (soft)", ErrOriginPullLoop, false},
		{"unrelated error", errors.New("something else"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsArrangeInfraError(tc.err); got != tc.want {
				t.Fatalf("IsArrangeInfraError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestDefaultArrange_NoDeps_Returns_DepsMissing(t *testing.T) {
	defaultArrangeDepsMu.Lock()
	prev := defaultArrangeDeps
	defaultArrangeDeps = nil
	defaultArrangeDepsMu.Unlock()
	t.Cleanup(func() {
		defaultArrangeDepsMu.Lock()
		defaultArrangeDeps = prev
		defaultArrangeDepsMu.Unlock()
	})

	if _, err := DefaultArrange(context.Background(), makeReq()); !errors.Is(err, ErrOriginPullDepsMissing) {
		t.Fatalf("want ErrOriginPullDepsMissing, got %v", err)
	}
}
