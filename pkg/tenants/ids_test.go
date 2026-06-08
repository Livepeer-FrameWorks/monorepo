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
