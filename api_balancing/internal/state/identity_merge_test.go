package state

import (
	"encoding/json"
	"testing"
)

// Identity merge contract: a non-empty incoming value updates the field, an
// empty incoming value never erases a known one — for local writers and for
// replicated snapshots alike. This is the guard against the prod incident
// where one cold-enrichment write left a stream identity-less forever and
// replication spread the emptiness cluster-wide.

func TestUpdateStreamFromBuffer_BackfillsIdentityOnExistingEntry(t *testing.T) {
	sm := NewStreamStateManager()

	// Born during a cold-enrichment window: no identity.
	if err := sm.UpdateStreamFromBuffer("s1", "s1", "", "", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	if ss := sm.GetStreamState("s1"); ss.NodeID != "" || ss.TenantID != "" {
		t.Fatalf("expected empty identity at birth, got %q/%q", ss.NodeID, ss.TenantID)
	}

	// Next buffer event carries identity: it must heal the entry.
	if err := sm.UpdateStreamFromBuffer("s1", "s1", "node-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	ss := sm.GetStreamState("s1")
	if ss.NodeID != "node-1" || ss.TenantID != "tenant-1" {
		t.Fatalf("identity not backfilled: %q/%q", ss.NodeID, ss.TenantID)
	}

	// A later identity-less event must not clobber.
	if err := sm.UpdateStreamFromBuffer("s1", "s1", "", "", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	ss = sm.GetStreamState("s1")
	if ss.NodeID != "node-1" || ss.TenantID != "tenant-1" {
		t.Fatalf("identity clobbered by empty write: %q/%q", ss.NodeID, ss.TenantID)
	}

	// The stream moving to another node must still update.
	if err := sm.UpdateStreamFromBuffer("s1", "s1", "node-2", "tenant-1", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	if ss := sm.GetStreamState("s1"); ss.NodeID != "node-2" {
		t.Fatalf("node move not applied: %q", ss.NodeID)
	}
}

func TestUpdateTrackListAndUserConnection_DoNotClobberTenant(t *testing.T) {
	sm := NewStreamStateManager()
	if err := sm.UpdateStreamFromBuffer("s2", "s2", "node-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatal(err)
	}

	sm.UpdateTrackList("s2", "node-1", "", "[]")
	if ss := sm.GetStreamState("s2"); ss.TenantID != "tenant-1" {
		t.Fatalf("UpdateTrackList clobbered tenant: %q", ss.TenantID)
	}

	sm.UpdateUserConnection("s2", "node-1", "", 1)
	if ss := sm.GetStreamState("s2"); ss.TenantID != "tenant-1" {
		t.Fatalf("UpdateUserConnection clobbered tenant: %q", ss.TenantID)
	}

	// And both still set it when they have it.
	sm2 := NewStreamStateManager()
	sm2.UpdateTrackList("s3", "node-1", "tenant-9", "[]")
	if ss := sm2.GetStreamState("s3"); ss.TenantID != "tenant-9" {
		t.Fatalf("UpdateTrackList did not set tenant: %q", ss.TenantID)
	}
}

func TestApplyRedisChange_StreamSnapshotMergesIdentityIgnoreEmpty(t *testing.T) {
	sm := NewStreamStateManager()
	if err := sm.UpdateStreamFromBuffer("s4", "s4", "node-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatal(err)
	}

	// A peer snapshot without identity (peer never resolved it) must not
	// erase locally-known identity.
	payload, _ := json.Marshal(&StreamState{InternalName: "s4", StreamName: "s4", Status: "live"})
	sm.applyRedisChange(StateChange{
		InstanceID: "peer",
		Entity:     StateEntityStream,
		Operation:  StateOpUpsert,
		StreamName: "s4",
		Payload:    payload,
	})
	ss := sm.GetStreamState("s4")
	if ss.NodeID != "node-1" || ss.TenantID != "tenant-1" {
		t.Fatalf("replicated empty snapshot erased identity: %q/%q", ss.NodeID, ss.TenantID)
	}

	// A peer snapshot WITH identity updates it.
	payload, _ = json.Marshal(&StreamState{InternalName: "s4", StreamName: "s4", NodeID: "node-2", TenantID: "tenant-1", Status: "live"})
	sm.applyRedisChange(StateChange{
		InstanceID: "peer",
		Entity:     StateEntityStream,
		Operation:  StateOpUpsert,
		StreamName: "s4",
		Payload:    payload,
	})
	if ss := sm.GetStreamState("s4"); ss.NodeID != "node-2" {
		t.Fatalf("replicated identity not applied: %q", ss.NodeID)
	}
}

func TestApplyRedisChange_NodeSnapshotMergesClusterIgnoreEmpty(t *testing.T) {
	sm := NewStreamStateManager()
	sm.SetNodeConnectionInfo(t.Context(), "node-1", "node-1.example", "tenant-1", "cluster-1", nil)

	payload, _ := json.Marshal(&NodeState{NodeID: "node-1"})
	sm.applyRedisChange(StateChange{
		InstanceID: "peer",
		Entity:     StateEntityNode,
		Operation:  StateOpUpsert,
		NodeID:     "node-1",
		Payload:    payload,
	})
	ns := sm.GetNodeState("node-1")
	if ns.ClusterID != "cluster-1" || ns.TenantID != "tenant-1" {
		t.Fatalf("replicated empty node snapshot erased identity: %q/%q", ns.ClusterID, ns.TenantID)
	}
}
