package tenants

import (
	"testing"

	"github.com/google/uuid"
)

// IsSystemTenant is an authorization boundary: it distinguishes the three
// reserved platform identities from real tenant/user UUIDs. A regression that
// drops one ID would let that identity be treated as an ordinary tenant
// (privilege confusion); one that matches too broadly would exempt real
// tenants from tenant-scoped checks. These cases pin the exact membership.
func TestIsSystemTenant(t *testing.T) {
	t.Setenv("SYSTEM_TENANT_ID", "")
	for _, id := range []uuid.UUID{ServiceAccountUserID, SystemTenantID, AnonymousTenantID} {
		if !IsSystemTenant(id) {
			t.Errorf("IsSystemTenant(%s) = false, want true (reserved id)", id)
		}
	}

	// A random tenant UUID is not a system tenant.
	if IsSystemTenant(uuid.MustParse("11111111-2222-3333-4444-555555555555")) {
		t.Error("random tenant id classified as system tenant")
	}

	// uuid.Nil is the all-zeros UUID, which is exactly ServiceAccountUserID.
	// This is intentional: the zero value of an unset uuid.UUID is the service
	// account, so an uninitialized id is treated as system, not as a tenant.
	if uuid.Nil != ServiceAccountUserID {
		t.Fatalf("precondition: uuid.Nil (%s) != ServiceAccountUserID (%s)", uuid.Nil, ServiceAccountUserID)
	}
	if !IsSystemTenant(uuid.Nil) {
		t.Error("uuid.Nil should be a system tenant (== ServiceAccountUserID)")
	}
}

func TestIsSystemTenantIncludesRuntimeIdentity(t *testing.T) {
	want := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	t.Setenv("SYSTEM_TENANT_ID", want.String())
	if !IsSystemTenant(want) {
		t.Fatalf("runtime system tenant %s was not classified as system", want)
	}
	if IsSystemTenant(SystemTenantID) {
		t.Fatalf("local/demo system tenant %s remained privileged after runtime authority changed", SystemTenantID)
	}
}

func TestRuntimeSystemTenantID(t *testing.T) {
	t.Run("reserved default", func(t *testing.T) {
		t.Setenv("SYSTEM_TENANT_ID", "")
		got, err := RuntimeSystemTenantID()
		if err != nil {
			t.Fatal(err)
		}
		if got != SystemTenantID {
			t.Fatalf("RuntimeSystemTenantID() = %s, want %s", got, SystemTenantID)
		}
	})

	t.Run("deployment identity", func(t *testing.T) {
		want := uuid.MustParse("11111111-2222-3333-4444-555555555555")
		t.Setenv("SYSTEM_TENANT_ID", want.String())
		got, err := RuntimeSystemTenantID()
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("RuntimeSystemTenantID() = %s, want %s", got, want)
		}
	})

	t.Run("invalid deployment identity", func(t *testing.T) {
		t.Setenv("SYSTEM_TENANT_ID", "not-a-uuid")
		if _, err := RuntimeSystemTenantID(); err == nil {
			t.Fatal("RuntimeSystemTenantID() error = nil, want invalid UUID error")
		}
	})
}
