package state

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBandwidthReservationRetainedAfterActivationAndOldReport(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	sm.ReservePendingViewer("attempt", "node", "stream")
	before := sm.GetNodeState("node").AddBandwidth
	sm.StartVirtualViewerByID("attempt", "node", "stream", "203.0.113.1")
	if got := sm.GetNodeState("node").AddBandwidth; got != before {
		t.Errorf("activation released unseen traffic: got=%d want=%d", got, before)
	}
	sm.ReconcileVirtualViewers("node", 1, 0)
	if got := sm.GetNodeState("node").AddBandwidth; got != before {
		t.Errorf("predating report released traffic: got=%d want=%d", got, before)
	}
}

func TestBandwidthReservationUsesStreamTopBitrate(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	sm.mu.Lock()
	sm.streams["stream"] = &StreamState{InternalName: "stream", Tracks: []StreamTrack{{Bitrate: 20000}}}
	sm.mu.Unlock()
	sm.ReservePendingViewer("attempt", "node", "stream")
	if got := sm.GetNodeState("node").AddBandwidth; got < 2750000 {
		t.Fatalf("20 Mbit/s stream reserved %d, want at least 2750000", got)
	}
}

func TestReturningNodeTakeoverRestoresBandwidthAdmission(t *testing.T) {
	for _, peer := range []bool{false, true} {
		t.Run(fmt.Sprintf("peer=%v", peer), func(t *testing.T) {
			sm := NewStreamStateManager()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeInfo("returning", "https://edge.test", true, nil, nil, "", "", nil)
			if err := sm.RecordNodeArtifactFence("returning", 1); err != nil {
				t.Fatal(err)
			}
			sm.MarkNodeDisconnected("returning")
			sm.mu.Lock()
			sm.nodes["returning"].LastUpdate = time.Now().Add(-nodeRemovalThreshold - time.Second)
			sm.mu.Unlock()
			if err := sm.evictDisconnectedNode("returning"); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			payload, err := json.Marshal(&NodeState{NodeID: "returning", IsHealthy: true, LastUpdate: now, LastHeartbeat: now, Capacity: DefaultNodeCapacity()})
			if err != nil {
				t.Fatal(err)
			}
			sm.applyRedisChange(StateChange{Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "returning", Payload: payload})
			if allowed, err := sm.PrepareViewerBandwidth(t.Context(), "unfenced", "returning", "stream"); err != nil || allowed {
				t.Fatalf("unfenced heartbeat revived an evicted node: %v %v", allowed, err)
			}
			if peer {
				for _, fence := range []int64{1, 2} {
					marker, err := json.Marshal(NodeArtifactSnapshot{NodeID: "returning", Fence: fence})
					if err != nil {
						t.Fatal(err)
					}
					sm.applyRedisChange(StateChange{Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "returning", Payload: marker})
					if fence == 1 {
						if allowed, _ := sm.PrepareViewerBandwidth(t.Context(), "old-fence", "returning", "stream"); allowed {
							t.Fatal("old control fence revived an evicted node")
						}
					}
				}
			} else if err := sm.RecordNodeArtifactFence("returning", 2); err != nil {
				t.Fatal(err)
			}
			if allowed, err := sm.PrepareViewerBandwidth(t.Context(), "new-fence", "returning", "stream"); err != nil || !allowed {
				t.Fatalf("returning node remains excluded after its authenticated takeover: %v %v", allowed, err)
			}
			sm.SetProbeVerified("returning", true)
			if !sm.GetNodeState("returning").ProbeVerified {
				t.Fatal("authenticated takeover still discards endpoint verification")
			}
		})
	}
}

func TestBandwidthReservationSharedAcrossReplicas(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	a, b := NewStreamStateManager(), NewStreamStateManager()
	defer a.Shutdown()
	defer b.Shutdown()
	a.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	b.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	for id, sm := range map[string]*StreamStateManager{"a": a, "b": b} {
		if err := sm.EnableRedisSync(context.Background(), store, id, logging.NewLogger()); err != nil {
			t.Fatal(err)
		}
	}
	a.ReservePendingViewer("attempt", "node", "stream")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if n := b.GetNodeState("node"); n != nil && n.AddBandwidth > 0 {
			return
		}
		time.Sleep(time.Millisecond * 10)
	}
	t.Fatal("replica B cannot see replica A's reservation")
}

func TestNodeBandwidthLedgerCrossReplicaActivationAndSettlement(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	a, b := NewStreamStateManager(), NewStreamStateManager()
	defer a.Shutdown()
	defer b.Shutdown()
	a.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	b.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	a.redisStore, b.redisStore = store, store
	a.instanceID, b.instanceID = "a", "b"
	ctx := context.Background()
	ok, err := a.ReserveNodeBandwidth(ctx, "attempt", "node", "stream", BandwidthEgress, 2750000)
	if !ok || err != nil {
		t.Fatalf("reserve=%v %v", ok, err)
	}
	ok, err = b.ActivateNodeBandwidth(ctx, "attempt", "node")
	if !ok || err != nil {
		t.Fatalf("activation on another replica=%v %v", ok, err)
	}
	now := time.Now()
	if err = b.SettleNodeBandwidth(ctx, "node", now.Add(-10*time.Second).UnixMilli(), now.UnixMilli(), now); err != nil {
		t.Fatal(err)
	}
	if got := b.GetNodeState("node").AddBandwidth; got != 2750000 {
		t.Fatalf("old window released held traffic: %d", got)
	}
	received := now.Add(12 * time.Second)
	if err = b.SettleNodeBandwidth(ctx, "node", received.Add(-10*time.Second).UnixMilli(), received.UnixMilli(), received); err != nil {
		t.Fatal(err)
	}
	if got := b.GetNodeState("node").AddBandwidth; got != 0 {
		t.Fatalf("complete window retained held traffic: %d", got)
	}
}

func TestNodeBandwidthLedgerRedisFailureUsesMemory(t *testing.T) {
	store, _, client := newRedisStateStore(t)
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	sm.redisStore = store
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := sm.ReserveNodeBandwidth(ctx, "first", "node", "stream", BandwidthEgress, 70000000); !ok || err != nil {
		t.Fatalf("fallback refused: %v %v", ok, err)
	}
	if ok, err := sm.ReserveNodeBandwidth(ctx, "second", "node", "stream", BandwidthEgress, 40000000); ok || err != nil {
		t.Fatalf("fallback failed to account first admission: %v %v", ok, err)
	}
	if got := sm.GetNodeState("node").AddBandwidth; got != 70000000 {
		t.Fatalf("fallback reserved=%d", got)
	}
}

func TestNodeBandwidthLedgerDirectionAndIdempotence(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	ctx := context.Background()
	for range 2 {
		if ok, err := sm.ReserveNodeBandwidth(ctx, "ingest", "node", "stream", BandwidthIngress, 1250000); !ok || err != nil {
			t.Fatalf("reserve=%v %v", ok, err)
		}
	}
	n := sm.GetNodeState("node")
	if n.IngressReservedBytesPerSec != 1250000 || n.AddBandwidth != 0 {
		t.Fatalf("wrong direction: %+v", n)
	}
	if ok, err := sm.ReserveNodeBandwidth(ctx, "ingest", "node", "other", BandwidthIngress, 1250000); ok || err != nil {
		t.Fatalf("rebound attempt=%v %v", ok, err)
	}
}

func TestNodeBandwidthLedgerRehydratesAfterRestart(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	a := NewStreamStateManager()
	defer a.Shutdown()
	a.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	a.redisStore = store
	if ok, err := a.ReserveNodeBandwidth(context.Background(), "attempt", "node", "stream", BandwidthEgress, 2750000); !ok || err != nil {
		t.Fatalf("reserve=%v %v", ok, err)
	}
	b := NewStreamStateManager()
	defer b.Shutdown()
	a.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	b.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	if err := b.EnableRedisSync(context.Background(), store, "b", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	if n := b.GetNodeState("node"); n == nil || n.AddBandwidth != 2750000 {
		t.Fatalf("restart lost reservations: %+v", n)
	}
}

func TestBandwidthReservationInvalidReportPreservesRoutingLoad(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	for _, update := range []*ipcpb.NodeLifecycleUpdate{
		{NodeId: "node", UpSpeed: 1000, ObservedValid: proto.Bool(true)},
		{NodeId: "node", UpSpeed: 0, ObservedValid: proto.Bool(false)},
	} {
		if err := sm.ApplyNodeLifecycle(context.Background(), update); err != nil {
			t.Fatal(err)
		}
		sm.ReconcileVirtualViewers("node", 1, update.GetUpSpeed())
	}
	if n := sm.GetNodeState("node"); n.UpSpeed != 1000 {
		t.Fatalf("invalid report erased routing load: %v", n.UpSpeed)
	}
}

func TestNodeBandwidthLedgerKeepsUint64Precision(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	budget := DefaultNodeCapacity()
	budget.EgressBudgetBytesPerSec = 20000000000000000
	sm.SetNodeCapacity("node", budget)
	sm.redisStore = store
	usable := capacity.Link{Budget: budget.EgressBudgetBytesPerSec}.Usable()
	ctx := context.Background()
	if ok, err := sm.ReserveNodeBandwidth(ctx, "too-big", "node", "stream", BandwidthEgress, usable+1); ok || err != nil {
		t.Fatalf("rounded above-cap demand admitted: %v %v", ok, err)
	}
	if ok, err := sm.ReserveNodeBandwidth(ctx, "__revision", "node", "stream", BandwidthEgress, usable); !ok || err != nil {
		t.Fatalf("exact boundary refused: %v %v", ok, err)
	}
	if ok, err := sm.ReserveNodeBandwidth(ctx, "extra", "node", "stream", BandwidthEgress, 1); ok || err != nil {
		t.Fatalf("one byte above cap admitted: %v %v", ok, err)
	}
	if err := sm.ReleaseNodeBandwidth(ctx, "__revision", "node"); err != nil {
		t.Fatal(err)
	}
	if got := sm.GetNodeState("node").AddBandwidth; got != 0 {
		t.Fatalf("exact release retained %d", got)
	}
}

func TestNodeBandwidthLedgerConcurrentReplicasDoNotOverAdmit(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	a, b := NewStreamStateManager(), NewStreamStateManager()
	defer a.Shutdown()
	defer b.Shutdown()
	a.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	b.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	a.redisStore, b.redisStore = store, store
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sm := a
			if i%2 == 1 {
				sm = b
			}
			ok, err := sm.ReserveNodeBandwidth(context.Background(), fmt.Sprintf("attempt-%d", i), "node", "stream", BandwidthEgress, 10000000)
			if err != nil {
				t.Error(err)
			}
			if ok {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 10 {
		t.Fatalf("admitted=%d, want 10 within the default 106250000 B/s usable budget", got)
	}
}

func TestPendingReleaseOnAnotherReplicaKeepsActiveReservation(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	a, b := NewStreamStateManager(), NewStreamStateManager()
	defer a.Shutdown()
	defer b.Shutdown()
	a.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	b.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	a.redisStore, b.redisStore = store, store
	if !a.ReservePendingViewer("attempt", "node", "stream") {
		t.Fatal("reserve")
	}
	if id, started := b.StartVirtualViewerByID("attempt", "node", "stream", ""); id == "" || !started {
		t.Fatal("activate on replica B")
	}
	a.ReleasePendingViewer("attempt", "node", "stream")
	if n := a.GetNodeState("node"); n.AddBandwidth == 0 {
		t.Fatal("replica A's stale pending correlation released replica B's active traffic")
	}
}

func TestNodeEvictionKeepsReconnectedOwnersReservations(t *testing.T) {
	store, _, client := newRedisStateStore(t)
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
	sm.redisStore = store
	sm.TouchNode("node", true)
	if !sm.ReservePendingViewer("attempt", "node", "stream") {
		t.Fatal("reserve")
	}
	sm.mu.Lock()
	sm.nodes["node"].LastHeartbeat = time.Time{}
	sm.nodes["node"].LastUpdate = time.Now().Add(-6 * time.Minute)
	sm.mu.Unlock()
	if ok, err := store.AcquireConnOwnerFenced(context.Background(), "node", "new-owner", "new-owner:9090", 2); err != nil || !ok {
		t.Fatalf("owner=%v %v", ok, err)
	}
	sm.checkStaleNodes()
	if n := sm.GetNodeState("node"); n == nil {
		t.Error("old replica evicted a node with a current connection owner")
	}
	keys := store.bandwidthKeys("node")
	if n, err := client.Exists(context.Background(), keys[0], keys[1], store.keyNode("node")).Result(); err != nil || n != 3 {
		t.Fatalf("reconnected owner lost durable node/reservation keys: count=%d err=%v", n, err)
	}
}

func TestIngressReservationUsesExactOperatorBudget(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("redis=%v", shared), func(t *testing.T) {
			sm := NewStreamStateManager()
			defer sm.Shutdown()
			sm.SetNodeInfo("node", "https://edge.test", true, nil, nil, "", "", nil)
			if shared {
				store, _, _ := newRedisStateStore(t)
				sm.redisStore = store
			}
			budget := DefaultNodeCapacity()
			budget.IngressBudgetBytesPerSec = 9007199254740993
			sm.SetNodeCapacity("edge", budget)
			usable := (capacity.Link{Budget: budget.IngressBudgetBytesPerSec}).Usable()
			admitted, err := sm.ReserveNodeBandwidth(t.Context(), "attempt", "edge", "stream", BandwidthIngress, usable)
			if err != nil || !admitted {
				t.Fatalf("exact usable ingress %d refused: admitted=%v err=%v", usable, admitted, err)
			}
		})
	}
}
