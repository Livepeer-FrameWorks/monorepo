package state

import "testing"

func TestTenantCapacityRegisterIdempotent(t *testing.T) {
	m := NewTenantCapacityManager()
	if n := m.RegisterStream("t1", "stream-a"); n != 1 {
		t.Errorf("first register: got %d want 1", n)
	}
	if n := m.RegisterStream("t1", "stream-a"); n != 1 {
		t.Errorf("duplicate register: got %d want 1 (idempotent)", n)
	}
	if n := m.RegisterStream("t1", "stream-b"); n != 2 {
		t.Errorf("second stream: got %d want 2", n)
	}
}

func TestTenantCapacityUnregisterRemoves(t *testing.T) {
	m := NewTenantCapacityManager()
	m.RegisterStream("t1", "stream-a")
	m.RegisterStream("t1", "stream-b")
	if n := m.UnregisterStream("t1", "stream-a"); n != 1 {
		t.Errorf("unregister: got %d want 1", n)
	}
	if n := m.UnregisterStream("t1", "stream-a"); n != 1 {
		t.Errorf("duplicate unregister: got %d want 1 (idempotent)", n)
	}
	if n := m.UnregisterStream("t1", "stream-b"); n != 0 {
		t.Errorf("last unregister: got %d want 0", n)
	}
}

func TestTenantCapacityIsolation(t *testing.T) {
	m := NewTenantCapacityManager()
	m.RegisterStream("t1", "stream-a")
	m.RegisterStream("t2", "stream-a")
	if m.CountStreams("t1") != 1 || m.CountStreams("t2") != 1 {
		t.Error("tenants must not share stream counts even with same internal_name")
	}
}

func TestTenantCapacityEmptyInputsNoop(t *testing.T) {
	m := NewTenantCapacityManager()
	if n := m.RegisterStream("", "stream-a"); n != 0 {
		t.Errorf("empty tenant ignored: got %d", n)
	}
	if n := m.RegisterStream("t1", ""); n != 0 {
		t.Errorf("empty stream ignored: got %d", n)
	}
	if n := m.CountStreams(""); n != 0 {
		t.Errorf("empty tenant count: got %d", n)
	}
}

func TestTenantCapacityViewerSet(t *testing.T) {
	m := NewTenantCapacityManager()
	m.RegisterViewer("t1", "sess-a")
	m.RegisterViewer("t1", "sess-b")
	m.RegisterViewer("t1", "sess-a") // duplicate
	if n := m.CountViewers("t1"); n != 2 {
		t.Errorf("viewer dedupe: got %d want 2", n)
	}
	m.UnregisterViewer("t1", "sess-a")
	if n := m.CountViewers("t1"); n != 1 {
		t.Errorf("after unregister: got %d want 1", n)
	}
}

func TestTenantCapacityResetsBucketOnEmpty(t *testing.T) {
	m := NewTenantCapacityManager()
	m.RegisterStream("t1", "stream-a")
	m.UnregisterStream("t1", "stream-a")
	// After last unregister the bucket should be cleared so the map doesn't
	// leak per-tenant memory for long-departed tenants.
	if _, present := m.streams["t1"]; present {
		t.Error("tenant bucket must be deleted when last entry removed")
	}
}

func TestTenantCapacityReconcileStreamsRemovesStaleEntries(t *testing.T) {
	m := NewTenantCapacityManager()
	m.RegisterStream("t1", "stale-a")
	m.RegisterStream("t1", "live-b")

	if n := m.ReconcileStreams("t1", []string{"live-b", "live-c"}); n != 2 {
		t.Fatalf("reconcile count = %d, want 2", n)
	}
	if m.HasStream("t1", "stale-a") {
		t.Error("reconcile must drop streams that no longer exist in stream state")
	}
	if !m.HasStream("t1", "live-b") || !m.HasStream("t1", "live-c") {
		t.Error("reconcile must preserve the current live stream set")
	}
}

func TestTenantCapacityReconcileStreamsClearsTenant(t *testing.T) {
	m := NewTenantCapacityManager()
	m.RegisterStream("t1", "stale-a")

	if n := m.ReconcileStreams("t1", nil); n != 0 {
		t.Fatalf("reconcile empty count = %d, want 0", n)
	}
	if _, present := m.streams["t1"]; present {
		t.Error("empty reconcile must clear the tenant bucket")
	}
}

func TestDefaultTenantCapacityReset(t *testing.T) {
	first := DefaultTenantCapacity()
	first.RegisterStream("t1", "stream-a")
	second := ResetDefaultTenantCapacityForTests()
	if second.CountStreams("t1") != 0 {
		t.Error("reset must return a fresh manager")
	}
	if DefaultTenantCapacity() != second {
		t.Error("DefaultTenantCapacity after reset must return the new instance")
	}
}
