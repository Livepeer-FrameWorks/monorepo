package state

import (
	"testing"
	"time"
)

func TestTenantViewerCapacityPersistsSessionCorrelationAndRefcountsFWCID(t *testing.T) {
	m := NewTenantCapacityManager()
	if allowed, added, count, err := m.TryRegisterViewer("t1", "node-a", "session-a", "fwcid-1", 1); err != nil || !allowed || !added || count != 1 {
		t.Fatalf("first viewer = allowed=%v added=%v count=%d err=%v", allowed, added, count, err)
	}
	if allowed, _, count, err := m.TryRegisterViewer("t1", "node-a", "session-b", "fwcid-1", 1); err != nil || !allowed || count != 1 {
		t.Fatalf("second Mist session for same viewer = allowed=%v count=%d err=%v", allowed, count, err)
	}
	if allowed, _, count, err := m.TryRegisterViewer("t1", "node-a", "session-c", "fwcid-2", 1); err != nil || allowed || count != 1 {
		t.Fatalf("second logical viewer = allowed=%v count=%d err=%v", allowed, count, err)
	}
	if capacityID, released, count, err := m.ReleaseViewerSession("t1", "node-a", "session-a"); err != nil || capacityID != "fwcid-1" || !released || count != 1 {
		t.Fatalf("first session release = capacity=%q released=%v count=%d err=%v", capacityID, released, count, err)
	}
	if capacityID, released, count, err := m.ReleaseViewerSession("t1", "node-a", "session-b"); err != nil || capacityID != "fwcid-1" || !released || count != 0 {
		t.Fatalf("last session release = capacity=%q released=%v count=%d err=%v", capacityID, released, count, err)
	}
}

func TestTenantViewerCapacityExpiresButCorrelationCanReactivate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewTenantCapacityManager()
	m.now = func() time.Time { return now }
	if allowed, _, _, err := m.TryRegisterViewer("t1", "node-a", "session-a", "fwcid-1", 1); err != nil || !allowed {
		t.Fatalf("reserve viewer: allowed=%v err=%v", allowed, err)
	}
	now = now.Add(tenantViewerCapacityLease + time.Second)
	if got := m.CountViewers("t1"); got != 0 {
		t.Fatalf("expired viewer count=%d, want 0", got)
	}
	if err := m.RenewViewerSession("t1", "node-a", "session-a", 1); err != nil {
		t.Fatalf("renew from client inventory: %v", err)
	}
	if got := m.CountViewers("t1"); got != 1 {
		t.Fatalf("reactivated viewer count=%d, want 1", got)
	}
	now = now.Add(tenantViewerCorrelationRetention + time.Second)
	if got := m.CountViewers("t1"); got != 0 {
		t.Fatalf("long-expired viewer count=%d, want 0", got)
	}
	if err := m.RenewViewerSession("t1", "node-a", "session-a", 1); err != nil {
		t.Fatalf("expired correlation renew: %v", err)
	}
	if got := m.CountViewers("t1"); got != 0 {
		t.Fatalf("expired correlation resurrected viewer count=%d", got)
	}
}

func TestTenantViewerCapacityRenewDoesNotReactivateAboveCap(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewTenantCapacityManager()
	m.now = func() time.Time { return now }
	if allowed, _, _, err := m.TryRegisterViewer("t1", "node-a", "session-a", "fwcid-1", 1); err != nil || !allowed {
		t.Fatalf("reserve first viewer: allowed=%v err=%v", allowed, err)
	}
	now = now.Add(tenantViewerCapacityLease + time.Second)
	if allowed, _, _, err := m.TryRegisterViewer("t1", "node-a", "session-b", "fwcid-2", 1); err != nil || !allowed {
		t.Fatalf("reserve replacement viewer: allowed=%v err=%v", allowed, err)
	}
	if err := m.RenewViewerSession("t1", "node-a", "session-a", 1); err != nil {
		t.Fatalf("renew expired first viewer: %v", err)
	}
	if got := m.CountViewers("t1"); got != 1 {
		t.Fatalf("viewer count after late inventory renew = %d, want cap 1", got)
	}
}

func TestTenantViewerLocalMirrorPrunesExpiredCorrelations(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := NewTenantCapacityManager()
	m.now = func() time.Time { return now }
	m.rememberViewer("expired-tenant", "node-a\x1fsession-a", "viewer-a", now.Add(time.Minute), now.Add(time.Minute))

	now = now.Add(tenantViewerLocalPruneInterval + time.Second)
	m.rememberViewer("active-tenant", "node-b\x1fsession-b", "viewer-b", now.Add(time.Minute), now.Add(time.Hour))

	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, exists := m.viewers["expired-tenant"]; exists {
		t.Fatal("expired Redis-mode mirror correlation was retained")
	}
	if len(m.viewers["active-tenant"]) != 1 {
		t.Fatalf("active mirror entries = %d, want 1", len(m.viewers["active-tenant"]))
	}
}

func TestDefaultTenantCapacityReset(t *testing.T) {
	first := DefaultTenantCapacity()
	if allowed, _, _, err := first.TryRegisterViewer("t1", "node-a", "session-a", "viewer-a", 1); err != nil || !allowed {
		t.Fatalf("seed default manager: allowed=%v err=%v", allowed, err)
	}
	second := ResetDefaultTenantCapacityForTests()
	if second.CountViewers("t1") != 0 || DefaultTenantCapacity() != second {
		t.Fatal("reset did not install a fresh default manager")
	}
}
