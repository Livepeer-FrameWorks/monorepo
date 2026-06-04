package state

import (
	"encoding/json"
	"testing"
	"time"
)

// Specs for the state-sync ordering invariant: an incoming peer change applies
// only when it is newer than local state.
const haOrderingRFC = "requires the state-sync ordering mechanism (docs/rfcs/foghorn-ha-ordering.md)"

// A stale peer snapshot must not roll back a fresher local node's IsHealthy.
func TestApplyRedisChange_StaleSnapshot_DoesNotRollBackHealth(t *testing.T) {
	t.Skip(haOrderingRFC)

	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Fresh local truth: node is healthy, updated now.
	sm.TouchNode("node-1", true)

	// A peer publishes an OLDER snapshot that still claims the node unhealthy.
	stale := NodeState{NodeID: "node-1", IsHealthy: false, LastUpdate: time.Now().Add(-time.Minute)}
	payload, _ := json.Marshal(stale)
	sm.applyRedisChange(StateChange{
		Entity:    StateEntityNode,
		Operation: StateOpUpsert,
		NodeID:    "node-1",
		Payload:   payload,
	})

	if ns := sm.GetNodeState("node-1"); ns == nil || !ns.IsHealthy {
		t.Fatal("stale peer snapshot rolled back fresher local IsHealthy=true")
	}
}

// A stale peer delete must not evict a fresher local node.
func TestApplyRedisChange_StaleDelete_DoesNotEvictFresherNode(t *testing.T) {
	t.Skip(haOrderingRFC)

	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.TouchNode("node-1", true) // fresh local node

	// A peer delete that logically predates the local node's last update.
	sm.applyRedisChange(StateChange{
		Entity:    StateEntityNode,
		Operation: StateOpDelete,
		NodeID:    "node-1",
	})

	if ns := sm.GetNodeState("node-1"); ns == nil {
		t.Fatal("stale peer delete evicted a fresher local node")
	}
}
