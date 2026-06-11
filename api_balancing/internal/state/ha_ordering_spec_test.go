package state

import (
	"encoding/json"
	"testing"
	"time"
)

// Acceptance specs for the state-sync ordering invariant (see
// docs/architecture/foghorn-ha.md, "Ordering and replay semantics"): an
// incoming peer change applies only when its changelog entry ID is above the
// entity's watermark, so a change logged before the write local state
// already reflects can never roll it back.

// A stale peer snapshot must not roll back a fresher local node's
// IsHealthy.
func TestApplyRedisChange_StaleSnapshot_DoesNotRollBackHealth(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Fresh local truth: node is healthy, its write-through landed at
	// changelog position 2-0.
	sm.TouchNode("node-1", true)
	sm.watermarks.Record(stateChangeKey(StateChange{Entity: StateEntityNode, NodeID: "node-1"}), "2-0")

	// A peer's OLDER snapshot (logged at 1-0) still claims the node
	// unhealthy: the watermark must drop it.
	stale := NodeState{NodeID: "node-1", IsHealthy: false, LastUpdate: time.Now().Add(-time.Minute)}
	payload, _ := json.Marshal(stale)
	sm.handleStateChangelogEntry("1-0", StateChange{
		InstanceID: "peer",
		Entity:     StateEntityNode,
		Operation:  StateOpUpsert,
		NodeID:     "node-1",
		Payload:    payload,
	})

	if ns := sm.GetNodeState("node-1"); ns == nil || !ns.IsHealthy {
		t.Fatal("stale peer snapshot rolled back fresher local IsHealthy=true")
	}

	// A genuinely newer snapshot (logged at 3-0) applies.
	newer := NodeState{NodeID: "node-1", IsHealthy: false, LastUpdate: time.Now()}
	payload, _ = json.Marshal(newer)
	sm.handleStateChangelogEntry("3-0", StateChange{
		InstanceID: "peer",
		Entity:     StateEntityNode,
		Operation:  StateOpUpsert,
		NodeID:     "node-1",
		Payload:    payload,
	})
	if ns := sm.GetNodeState("node-1"); ns == nil || ns.IsHealthy {
		t.Fatal("newer peer snapshot was not applied")
	}
}

// A stale peer delete must not evict a fresher local node.
func TestApplyRedisChange_StaleDelete_DoesNotEvictFresherNode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Fresh local node, write-through logged at 2-0.
	sm.TouchNode("node-1", true)
	sm.watermarks.Record(stateChangeKey(StateChange{Entity: StateEntityNode, NodeID: "node-1"}), "2-0")

	// A peer delete that was logged BEFORE the local write (1-0).
	sm.handleStateChangelogEntry("1-0", StateChange{
		InstanceID: "peer",
		Entity:     StateEntityNode,
		Operation:  StateOpDelete,
		NodeID:     "node-1",
	})

	if ns := sm.GetNodeState("node-1"); ns == nil {
		t.Fatal("stale peer delete evicted a fresher local node")
	}
}
