package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// OperationalMode is multi-writer and travels via its own changelog entity
// (node_mode) with an independent watermark; node snapshots never overwrite
// a locally-known mode. These specs replay the exact race the design closes:
// a heartbeat snapshot marshaled BEFORE a mode change but logged AFTER it.

func TestNodeMode_HeartbeatSnapshotRaceReplay(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("node-1", "https://n1", true, nil, nil, "", "", nil)

	// Peer mode change lands at 100-0.
	modePayload, _ := json.Marshal(nodeModeRecord{NodeID: "node-1", Mode: NodeModeDraining, SetBy: "operator", SetAt: time.Now()})
	sm.handleStateChangelogEntry("100-0", StateChange{
		InstanceID: "peer-a", Entity: StateEntityNodeMode, Operation: StateOpUpsert, NodeID: "node-1", Payload: modePayload,
	})
	if got := sm.GetNodeOperationalMode("node-1"); got != NodeModeDraining {
		t.Fatalf("mode after node_mode entry = %q, want draining", got)
	}

	// The conn-owner's in-flight heartbeat snapshot — marshaled before it
	// applied the mode change — lands at the NEWER position 105-0 still
	// carrying the old mode. The mode must survive; the metrics must apply.
	snap := NodeState{NodeID: "node-1", OperationalMode: NodeModeNormal, CPU: 42, IsHealthy: true, LastUpdate: time.Now()}
	snapPayload, _ := json.Marshal(snap)
	sm.handleStateChangelogEntry("105-0", StateChange{
		InstanceID: "peer-b", Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-1", Payload: snapPayload,
	})
	ns := sm.GetNodeState("node-1")
	if ns == nil || ns.OperationalMode != NodeModeDraining {
		t.Fatalf("heartbeat snapshot resurrected old mode: %+v", ns)
	}
	if ns.CPU != 42 {
		t.Fatalf("snapshot metrics not applied: CPU=%v want 42", ns.CPU)
	}

	// A stale node_mode entry (90-0, below the 100-0 watermark) is dropped.
	stalePayload, _ := json.Marshal(nodeModeRecord{NodeID: "node-1", Mode: NodeModeMaintenance})
	sm.handleStateChangelogEntry("90-0", StateChange{
		InstanceID: "peer-a", Entity: StateEntityNodeMode, Operation: StateOpUpsert, NodeID: "node-1", Payload: stalePayload,
	})
	if got := sm.GetNodeOperationalMode("node-1"); got != NodeModeDraining {
		t.Fatalf("stale node_mode entry applied: %q", got)
	}

	// A genuinely newer node_mode entry (110-0) applies.
	newerPayload, _ := json.Marshal(nodeModeRecord{NodeID: "node-1", Mode: NodeModeNormal})
	sm.handleStateChangelogEntry("110-0", StateChange{
		InstanceID: "peer-a", Entity: StateEntityNodeMode, Operation: StateOpUpsert, NodeID: "node-1", Payload: newerPayload,
	})
	if got := sm.GetNodeOperationalMode("node-1"); got != NodeModeNormal {
		t.Fatalf("newer node_mode entry not applied: %q", got)
	}
}

// A node first learned from a peer snapshot adopts that snapshot's mode
// (mixed-version bootstrap); once the node is known locally, snapshot-borne
// mode changes are ignored (the dedicated entity owns them).
func TestNodeMode_MixedVersion_SnapshotFillsOnlyUnknownNode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	first := NodeState{NodeID: "node-x", OperationalMode: NodeModeMaintenance, IsHealthy: true, LastUpdate: time.Now()}
	p1, _ := json.Marshal(first)
	sm.handleStateChangelogEntry("10-0", StateChange{
		InstanceID: "old-peer", Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-x", Payload: p1,
	})
	if got := sm.GetNodeOperationalMode("node-x"); got != NodeModeMaintenance {
		t.Fatalf("first-sight snapshot mode not adopted: %q", got)
	}

	second := NodeState{NodeID: "node-x", OperationalMode: NodeModeNormal, IsHealthy: true, LastUpdate: time.Now()}
	p2, _ := json.Marshal(second)
	sm.handleStateChangelogEntry("11-0", StateChange{
		InstanceID: "old-peer", Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-x", Payload: p2,
	})
	if got := sm.GetNodeOperationalMode("node-x"); got != NodeModeMaintenance {
		t.Fatalf("known node's mode overwritten by snapshot: %q", got)
	}
}

// Write-through + rehydrate: the dedicated node_mode key wins over a stale
// mode embedded in the node JSON, both for a fresh instance's rehydrate and
// for live peers via the changelog.
func TestNodeMode_WriteThroughAndRehydrate(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	smA := NewStreamStateManager()
	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close() })
	storeA := NewRedisStateStore(clientA, "test-cluster")
	if err := smA.EnableRedisSync(context.Background(), storeA, "instance-a", logger); err != nil {
		t.Fatalf("EnableRedisSync A: %v", err)
	}
	t.Cleanup(smA.Shutdown)

	smA.SetNodeInfo("node-1", "https://n1", true, nil, nil, "", "", nil)
	if err := smA.SetNodeOperationalMode(context.Background(), "node-1", NodeModeDraining, "test"); err != nil {
		t.Fatalf("SetNodeOperationalMode: %v", err)
	}

	// Sabotage: overwrite the node write-through key with a snapshot whose
	// embedded mode is stale (exactly what an in-flight heartbeat does).
	staleNode := &NodeState{NodeID: "node-1", BaseURL: "https://n1", OperationalMode: NodeModeNormal, IsHealthy: true, LastUpdate: time.Now()}
	if err := storeA.SetNode("node-1", staleNode); err != nil {
		t.Fatalf("SetNode: %v", err)
	}

	// A fresh instance rehydrates: the dedicated key must win.
	smB := NewStreamStateManager()
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	storeB := NewRedisStateStore(clientB, "test-cluster")
	if err := smB.EnableRedisSync(context.Background(), storeB, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync B: %v", err)
	}
	t.Cleanup(smB.Shutdown)

	if got := smB.GetNodeOperationalMode("node-1"); got != NodeModeDraining {
		t.Fatalf("rehydrated mode = %q, want draining (dedicated key over node JSON)", got)
	}

	// Live propagation: a mode change on A reaches B via the changelog.
	if err := smA.SetNodeOperationalMode(context.Background(), "node-1", NodeModeMaintenance, "test"); err != nil {
		t.Fatalf("SetNodeOperationalMode 2: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := smB.GetNodeOperationalMode("node-1"); got == NodeModeMaintenance {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("mode change did not propagate to B: %q", smB.GetNodeOperationalMode("node-1"))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// AddBandwidth/PendingRedirects are local-only soft state: peer snapshots
// must neither clobber the local penalty nor seed one on a fresh replica.
func TestAddBandwidth_PeerSnapshotDoesNotClobberLocalPenalty(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	configureTestNode(sm, "node-1")

	sm.mu.Lock()
	sm.nodes["node-1"].EstBandwidthPerUser = 256
	sm.mu.Unlock()
	sm.CreateVirtualViewer("node-1", "s1", "203.0.113.10")

	sm.mu.RLock()
	localPenalty := sm.nodes["node-1"].AddBandwidth
	localPending := sm.nodes["node-1"].PendingRedirects
	localBinHost := sm.nodes["node-1"].BinHost
	sm.mu.RUnlock()
	if localPenalty == 0 || localPending == 0 {
		t.Fatalf("test setup: expected local penalty, got add=%d pending=%d", localPenalty, localPending)
	}

	// Peer snapshot with zero penalty and changed CPU: CPU applies, the
	// local penalty (and the json:"-" scoring inputs) survive.
	snap := NodeState{NodeID: "node-1", BaseURL: "node-1", OperationalMode: NodeModeNormal, CPU: 77, BWLimit: 1024 * 1024, IsHealthy: true, LastUpdate: time.Now()}
	payload, _ := json.Marshal(snap)
	sm.handleStateChangelogEntry("50-0", StateChange{
		InstanceID: "peer", Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-1", Payload: payload,
	})

	ns := sm.GetNodeState("node-1")
	if ns == nil || ns.CPU != 77 {
		t.Fatalf("peer snapshot metrics not applied: %+v", ns)
	}
	if ns.AddBandwidth != localPenalty || ns.PendingRedirects != localPending {
		t.Fatalf("local penalty clobbered: add=%d pending=%d, want %d/%d", ns.AddBandwidth, ns.PendingRedirects, localPenalty, localPending)
	}
	if ns.BinHost != localBinHost {
		t.Fatal("BinHost zeroed by peer snapshot apply")
	}
	// BWAvailable reflects the preserved penalty (recomputed against it).
	expected := uint64(1024*1024) - localPenalty
	if ns.BWAvailable != expected {
		t.Fatalf("BWAvailable = %d, want %d (BWLimit - preserved penalty)", ns.BWAvailable, expected)
	}
}

func TestAddBandwidth_FreshReplicaDoesNotAdoptPeerPenalty(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	snap := NodeState{NodeID: "node-new", AddBandwidth: 500000, PendingRedirects: 7, IsHealthy: true, LastUpdate: time.Now()}
	payload, _ := json.Marshal(snap)
	sm.handleStateChangelogEntry("60-0", StateChange{
		InstanceID: "peer", Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-new", Payload: payload,
	})

	ns := sm.GetNodeState("node-new")
	if ns == nil {
		t.Fatal("node not created from peer snapshot")
	}
	if ns.AddBandwidth != 0 || ns.PendingRedirects != 0 {
		t.Fatalf("fresh replica adopted peer's penalty: add=%d pending=%d, want 0/0", ns.AddBandwidth, ns.PendingRedirects)
	}
}
