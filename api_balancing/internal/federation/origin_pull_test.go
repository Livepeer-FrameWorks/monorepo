package federation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestArrangeCoordinationFailureIsNotCapacityOrContention(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:18009"})
	if err := d.Cache.client.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullStateUnavailable) || !IsArrangeInfraError(err) {
		t.Fatalf("unavailable cache treated as a soft/capacity refusal: %v", err)
	}
	if len(fed.calls) != 0 {
		t.Fatal("source notified without a coordination lease")
	}
}

func TestArrangeCorruptLoopStateFailsClosed(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:18009"})
	req := makeReq()
	if err := d.Cache.client.Set(context.Background(), d.Cache.keyRemoteReplication(req.InternalName, req.RemoteCluster), "not-json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	_, err := d.ArrangeOriginPull(context.Background(), req)
	if !errors.Is(err, ErrOriginPullStateUnavailable) || !IsArrangeInfraError(err) {
		t.Fatalf("corrupt loop state accepted as an empty pool: %v", err)
	}
	if len(fed.calls) != 0 {
		t.Fatal("source notified with unknown loop-prevention state")
	}
}

type fakeNotifyFedClient struct {
	mu        sync.Mutex
	calls     []*foghornfederationpb.OriginPullNotification
	addresses []string
	peerIDs   []string
	acks      []*foghornfederationpb.OriginPullAck
	errs      []error
	mutateAck func(*foghornfederationpb.OriginPullAck)
}

func (f *fakeNotifyFedClient) NotifyOriginPull(_ context.Context, peerID, address string, req *foghornfederationpb.OriginPullNotification) (*foghornfederationpb.OriginPullAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := len(f.calls)
	f.calls = append(f.calls, req)
	f.addresses = append(f.addresses, address)
	f.peerIDs = append(f.peerIDs, peerID)
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.acks) {
		return f.acks[idx], nil
	}
	ack := &foghornfederationpb.OriginPullAck{Accepted: true, DtscUrl: "dtsc://peer/" + req.StreamName,
		SourceGeneration: req.GetSourceGeneration(), SourceRevision: req.GetSourceRevision(), AttemptId: req.GetAttemptId(),
		SourceNodeId: req.GetSourceNodeId(), TenantId: req.GetTenantId(), DestClusterId: req.GetDestClusterId(), DestNodeId: req.GetDestNodeId(),
		SourceCellId: req.GetSourceCellId(), SourceClusterId: req.GetSourceClusterId(),
	}
	if f.mutateAck != nil {
		f.mutateAck(ack)
	}
	return ack, nil
}

type fakePeerResolver struct{ addrs map[string]string }

func (f *fakePeerResolver) GetPeerAddr(clusterID string) string { return f.addrs[clusterID] }

// freshRegistry installs a temporary StreamRegistry for the duration of the
// test and restores the prior global on cleanup.
func freshRegistry(t *testing.T) *control.StreamRegistry {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
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
		Registry:     control.StreamRegistryInstance,
		PeerResolver: &fakePeerResolver{addrs: addrs},
		CellAddress:  func(cellID string) string { return addrs[cellID] },
		FedClient:    fed,
		InstanceID:   "foghorn-test",
		Logger:       testLogger(),
	}
}

// makeDepsAt binds arrangement to a fixture clock, so acceptance timestamps and
// the window that reads them come from the same time source.
func makeDepsAt(t *testing.T, fed *fakeNotifyFedClient, addrs map[string]string, now func() time.Time) *ArrangeOriginPullDeps {
	t.Helper()
	deps := makeDeps(t, fed, addrs)
	deps.Now = now
	return deps
}

func makeReq() ArrangeOriginPullRequest {
	return ArrangeOriginPullRequest{
		InternalName:    "stream-1",
		Remote:          &foghornfederationpb.EdgeCandidate{NodeId: "remote-node", BaseUrl: "https://peer"},
		RemoteCluster:   "cluster-peer",
		TenantID:        "tenant-1",
		DestClusterID:   "cluster-local",
		DestNodeID:      "local-edge",
		DestNodeBaseURL: "local-edge.cluster-local",
	}
}

// RemoteCluster names a control cell on the placement and cross-cluster DVR
// paths, and a media cluster on the legacy /source path. Only the cell resolver
// is keyed by cell, so a caller that names one without carrying a placement
// binding — the DVR path — must still reach its peer. Selecting the resolver
// from whether a binding is present sent every such caller through the
// cluster-keyed map, where a cell can only miss.
func TestArrangeResolvesACellNamedPeerWithoutAPlacementBinding(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	cache, _ := setupTestCache(t)
	deps := &ArrangeOriginPullDeps{
		Cache:    cache,
		Registry: control.StreamRegistryInstance,
		// Keyed by media cluster, as the real peer map is: it does not know the cell.
		PeerResolver: &fakePeerResolver{addrs: map[string]string{"peer-media-cluster": "peer:443"}},
		CellAddress:  func(cellID string) string { return map[string]string{"cluster-peer": "peer:443"}[cellID] },
		FedClient:    fed,
		InstanceID:   "foghorn-test",
		Logger:       testLogger(),
	}

	if _, err := deps.ArrangeOriginPull(context.Background(), makeReq()); err != nil {
		t.Fatalf("a cell-named peer was unreachable without a placement binding: %v", err)
	}
	if len(fed.calls) != 1 {
		t.Fatalf("NotifyOriginPull calls = %+v", fed.calls)
	}
}

func TestArrangePlacementReuseMarksExistingPullBeforeReturning(t *testing.T) {
	registry := freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.Remote.ClusterId = "remote-media"
	req.SourceGeneration, req.SourceRevision = "generation", 1
	ctx := context.Background()
	first, err := deps.ArrangeOriginPull(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	pull, found, err := registry.CurrentInboundPull(ctx, req.InternalName, req.DestNodeID)
	if err != nil || !found || pull.PlacementRequired {
		t.Fatal("legacy fixture was not an unmarked pull")
	}
	req.DestinationFence = 123
	req.BindPull = func(binding *PlacementPullBinding) error {
		if binding.AttemptID != first.AttemptID {
			t.Fatal("reused physical attempt changed")
		}
		return nil
	}
	second, err := deps.ArrangeOriginPull(ctx, req)
	if err != nil || !second.Reused || second.PullDTSCURL != first.PullDTSCURL || len(fed.calls) != 1 {
		t.Fatalf("placement did not adopt existing physical pull: %+v, %v", second, err)
	}
	pull, found, err = registry.CurrentInboundPull(ctx, req.InternalName, req.DestNodeID)
	if err != nil || !found || !pull.PlacementRequired || pull.PlacementDemand != "" {
		t.Fatalf("reused URL escaped without source admission requirement: %+v, %v", pull, err)
	}
}

func TestArrangeUsesOneExplicitRegistryAcrossSourceNotification(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprint(bound), func(t *testing.T) {
			ambient := freshRegistry(t)
			registry := control.NewStreamRegistry(nil, "explicit-cell", time.Minute)
			fed := &fakeNotifyFedClient{mutateAck: func(*foghornfederationpb.OriginPullAck) {
				control.SetStreamRegistry(nil)
			}}
			deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
			deps.Registry = registry
			req := makeReq()
			req.Remote.ClusterId = "remote-media"
			req.SourceGeneration, req.SourceRevision = "generation", 1
			if bound {
				req.DestinationFence = 123
				req.BindPull = func(*PlacementPullBinding) error { return nil }
			}
			first, err := deps.ArrangeOriginPull(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			second, err := deps.ArrangeOriginPull(t.Context(), req)
			if err != nil || !second.Reused || second.AttemptID != first.AttemptID || len(fed.calls) != 1 {
				t.Fatalf("ambient registry changed arrangement: %+v, %v", second, err)
			}
			pull, found, err := registry.CurrentInboundPull(t.Context(), req.InternalName, req.DestNodeID)
			if err != nil || !found || pull.PlacementRequired != bound {
				t.Fatalf("explicit registry lost source binding: %+v, %v", pull, err)
			}
			if _, found, err := ambient.CurrentInboundPull(t.Context(), req.InternalName, req.DestNodeID); err != nil || found {
				t.Fatal("arrangement wrote another registry")
			}
		})
	}
}

func TestArrangeBoundPullCannotBorrowLegacyRegistry(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	deps := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	deps.Registry = nil
	req := makeReq()
	req.BindPull = func(*PlacementPullBinding) error { t.Fatal("missing registry reached receipt binding"); return nil }
	if response, err := deps.ArrangeOriginPull(t.Context(), req); !errors.Is(err, ErrOriginPullRegistryNil) || response != nil || len(fed.calls) != 0 {
		t.Fatalf("bound request borrowed ambient registry: %+v, %v", response, err)
	}
	req.BindPull = nil
	if response, err := deps.ArrangeOriginPull(t.Context(), req); err != nil || response == nil {
		t.Fatalf("legacy registry fallback failed: %+v, %v", response, err)
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
	r.UpsertFederatedSource("cluster-peer", control.StreamEntry{
		InternalName: "stream-1", OriginClusterID: "cluster-origin",
	}, control.Location{IsLiveNow: true})
	sm := state.DefaultManager()
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	var emitted *ipcpb.FederationEventData
	d.EventEmitter = func(data *ipcpb.FederationEventData) { emitted = data }

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
	if inst, exists := sm.GetStreamInstances("stream-1")["local-edge"]; exists {
		t.Fatalf("accepted pull fabricated a media observation: %+v", inst)
	}
	sm.UpdateNodeStats("stream-1", "local-edge", 3, 1, 500, 1000, true)
	observed := sm.GetStreamInstances("stream-1")["local-edge"]
	if reused, err := d.ArrangeOriginPull(context.Background(), makeReq()); err != nil || !reused.Reused {
		t.Fatalf("accepted pull could not be reused after media observation: %+v %v", reused, err)
	}
	if got := sm.GetStreamInstances("stream-1")["local-edge"]; got.Status != "live" || got.Inputs != 1 || got.BytesDown != 1000 || !got.LastUpdate.Equal(observed.LastUpdate) {
		t.Fatalf("preparation changed node-reported media state: %+v", got)
	}
	if len(fed.calls) != 1 || fed.calls[0].StreamName != "stream-1" || fed.calls[0].GetDestClusterId() != "cluster-local" {
		t.Fatalf("NotifyOriginPull calls = %+v", fed.calls)
	}
	if emitted == nil || emitted.GetStreamTenantId() != "tenant-1" || emitted.GetOriginClusterId() != "cluster-origin" {
		t.Fatalf("federation attribution = %+v", emitted)
	}
	// Federation events are persisted and read back by operators. The arranged
	// event carries the media path only; the credential rides on the URL handed
	// to the destination, never into stored state.
	for _, marker := range []string{"token=", "fwsrc."} {
		if strings.Contains(emitted.GetDtscUrl(), marker) {
			t.Fatalf("ORIGIN_PULL_ARRANGED leaked %q: %s", marker, emitted.GetDtscUrl())
		}
	}
}

func TestArrange_DestinationNodeClusterCannotBeSubstitutedByProcessCluster(t *testing.T) {
	freshRegistry(t)
	sm := state.DefaultManager()
	sm.SetNodeConnectionInfo(context.Background(), "local-edge", "local-edge.example", "", "tenant-media-b", nil)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.DestClusterID = "tenant-media-b"

	if _, err := d.ArrangeOriginPull(context.Background(), req); err != nil {
		t.Fatalf("arrange origin pull: %v", err)
	}
	if len(fed.calls) != 1 || fed.calls[0].GetDestClusterId() != "tenant-media-b" {
		t.Fatalf("destination cluster was inferred from Foghorn process: %+v", fed.calls)
	}
}

func TestArrange_DestinationNodeClusterMismatchIsRefused(t *testing.T) {
	freshRegistry(t)
	sm := state.DefaultManager()
	sm.SetNodeConnectionInfo(context.Background(), "local-edge", "local-edge.example", "", "tenant-media-b", nil)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.DestClusterID = "tenant-media-a"

	if _, err := d.ArrangeOriginPull(context.Background(), req); !errors.Is(err, ErrOriginPullNoDest) {
		t.Fatalf("want destination-cluster mismatch refusal, got %v", err)
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
		acks: []*foghornfederationpb.OriginPullAck{{Accepted: false, Reason: "peer refused"}},
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
	req.LBPicker = func(context.Context, float64, float64, string) (string, string, string, error) {
		return "", "", "", errors.New("no edges")
	}
	_, err := d.ArrangeOriginPull(context.Background(), req)
	if err == nil {
		t.Fatal("expected LB error")
	}
	if errors.Is(err, ErrOriginPullNoDest) {
		t.Fatalf("LB error should not be classified as NoDest, got %v", err)
	}
}

func TestArrange_LBPickerCarriesSelectedNodeCluster(t *testing.T) {
	freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	req := makeReq()
	req.DestNodeID = ""
	req.DestClusterID = ""
	req.LBPicker = func(context.Context, float64, float64, string) (string, string, string, error) {
		return "local-edge.example", "local-edge", "cluster-local", nil
	}
	if _, err := d.ArrangeOriginPull(context.Background(), req); err != nil {
		t.Fatalf("arrange origin pull: %v", err)
	}
	if len(fed.calls) != 1 || fed.calls[0].GetDestClusterId() != "cluster-local" {
		t.Fatalf("selected node cluster was lost: %+v", fed.calls)
	}
}

func TestArrange_LoopPreventionRejected(t *testing.T) {
	freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{}, map[string]string{"cluster-peer": "peer:443"})

	// Seed cache with an existing remote replication FROM cluster-peer
	// (i.e. they are already pulling this stream FROM us). Trying to
	// pull from them in reverse must be refused.
	// The guard matches on cell identity, which is what an arrangement names.
	// ClusterID here is the media cluster hosting their replica.
	if err := d.Cache.SetRemoteReplication(context.Background(), "cluster-peer", &RemoteReplicationEntry{
		StreamName:    "stream-1",
		ClusterID:     "their-media-cluster",
		ControlCellID: "cluster-peer",
		NodeID:        "their-edge",
		Available:     true,
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
	markReplicatingForTest(t, r, "stream-1", "cluster-peer", "dtsc://peer/stream-1", "local-edge", "local-edge.cluster-local", "remote-node")

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
	if !d.Cache.TryAcquireOriginPullLock(context.Background(), originPullDestinationKey("stream-1", "local-edge"), "other-foghorn") {
		t.Fatal("seed lock should succeed")
	}
	t.Cleanup(func() {
		d.Cache.ReleaseOriginPullLock(context.Background(), originPullDestinationKey("stream-1", "local-edge"), "other-foghorn")
	})

	_, err := d.ArrangeOriginPull(context.Background(), makeReq())
	if !errors.Is(err, ErrOriginPullLockContention) {
		t.Fatalf("want ErrOriginPullLockContention, got %v", err)
	}
}

func TestArrangeKeepsEachSelectedDestination(t *testing.T) {
	r := freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	ctx := context.Background()
	for _, node := range []string{"edge-us-east", "edge-us-west"} {
		req := makeReq()
		req.DestNodeID, req.DestNodeBaseURL = node, "https://"+node
		result, err := d.ArrangeOriginPull(ctx, req)
		if err != nil || result == nil || result.DestNodeID != node || result.Reused {
			t.Fatalf("selected destination replaced: %+v %v", result, err)
		}
		loc, ok := r.LocalReplicationForNode(ctx, req.InternalName, node)
		if !ok || loc.DestNodeID != node || loc.PullDTSCURL == "" {
			t.Fatalf("destination not tracked: %+v %v", loc, ok)
		}
	}
	if len(fed.calls) != 2 || len(r.AllLocalReplications()["stream-1"]) != 2 {
		t.Fatal("second destination reused the first destination's preparation")
	}
	req := makeReq()
	req.DestNodeID = "edge-us-west"
	result, err := d.ArrangeOriginPull(ctx, req)
	if err != nil || !result.Reused || result.DestNodeID != "edge-us-west" || len(fed.calls) != 2 {
		t.Fatalf("exact retry not reused: %+v %v", result, err)
	}
}

func TestArrangeDistinctDestinationsDoNotShareLease(t *testing.T) {
	r := freshRegistry(t)
	fed := &fakeNotifyFedClient{}
	d := makeDeps(t, fed, map[string]string{"cluster-peer": "peer:443"})
	if !d.Cache.TryAcquireOriginPullLock(context.Background(), originPullDestinationKey("stream-1", "edge-east"), "other") {
		t.Fatal("lease setup failed")
	}
	req := makeReq()
	req.DestNodeID = "edge-west"
	result, err := d.ArrangeOriginPull(context.Background(), req)
	if err != nil || result.DestNodeID != "edge-west" || len(r.AllLocalReplications()["stream-1"]) != 1 {
		t.Fatalf("unrelated destination blocked: %+v %v", result, err)
	}
}

func TestArrangeEmptySourceURLIsNotPrepared(t *testing.T) {
	r := freshRegistry(t)
	d := makeDeps(t, &fakeNotifyFedClient{acks: []*foghornfederationpb.OriginPullAck{{Accepted: true}}}, map[string]string{"cluster-peer": "peer:443"})
	if result, err := d.ArrangeOriginPull(context.Background(), makeReq()); result != nil || !errors.Is(err, ErrOriginPullNotifyFailed) {
		t.Fatalf("empty source accepted: %+v %v", result, err)
	}
	if len(r.AllLocalReplications()) != 0 {
		t.Fatal("empty source became a pending destination")
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

// markReplicatingForTest builds the pre-placement replication record these
// fixtures rely on: an inbound pull with no owner tenant and no destination
// cluster, which prepared-source admission can never accept.
func markReplicatingForTest(t *testing.T, r *control.StreamRegistry, internalName, peerClusterID, pullDTSCURL, destNodeID, destNodeBaseURL, pullSourceNodeID string) {
	t.Helper()
	if _, err := r.RecordInboundPull(context.Background(), internalName, control.InboundPull{
		SourceClusterID: peerClusterID, SourceNodeID: pullSourceNodeID,
		DestNodeID: destNodeID, DestNodeBaseURL: destNodeBaseURL, DTSCURL: pullDTSCURL,
	}); err != nil {
		t.Fatalf("record inbound pull for %s: %v", internalName, err)
	}
}
