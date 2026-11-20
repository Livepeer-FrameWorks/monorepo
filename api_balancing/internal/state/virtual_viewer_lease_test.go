package state

import (
	"testing"
	"time"
)

func TestVirtualViewerLeaseExpiresAfterTwoMissingClientSweeps(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	for _, name := range []string{"missing-session", "never-attached", "fresh"} {
		id := sm.CreateVirtualViewer("node", "stream", name)
		if !sm.ConfirmVirtualViewerByID(id, "node", "stream", name, "") {
			t.Fatal("viewer did not activate")
		}
		if name != "fresh" {
			sm.mu.Lock()
			sm.virtualViewers[id].ConnectTime = time.Now().Add(-121 * time.Second)
			sm.mu.Unlock()
		}
		if name == "missing-session" {
			sm.mu.Lock()
			sm.virtualViewers[id].addMistSessionID("gone")
			sm.mu.Unlock()
		}
	}
	sm.ReconcileVirtualViewers("node", 1, 0)
	if got := sm.GetVirtualViewerStats(); got.Active != 1 || got.Disconnected != 2 {
		t.Fatalf("leases after two missed sweeps: %+v, want active 1 disconnected 2", got)
	}
}

func TestVirtualViewerLeaseRenewsAttachedSessionOnly(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	id := sm.CreateVirtualViewer("node", "stream", "ip")
	if !sm.ConfirmVirtualViewerByID(id, "node", "stream", "ip", "present") {
		t.Fatal("viewer did not activate")
	}
	sm.mu.Lock()
	sm.virtualViewers[id].ConnectTime = time.Now().Add(-121 * time.Second)
	sm.mu.Unlock()
	if sm.RenewVirtualViewerSession("node", "unknown") {
		t.Fatal("unknown session renewed a viewer")
	}
	if !sm.RenewVirtualViewerSession("node", "present") {
		t.Fatal("attached session did not renew its viewer")
	}
	sm.ReconcileVirtualViewers("node", 1, 0)
	if got := sm.GetVirtualViewerStats(); got.Active != 1 || got.Disconnected != 0 {
		t.Fatalf("renewed lease expired: %+v", got)
	}
}
