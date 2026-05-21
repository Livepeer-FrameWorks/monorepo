package resolvers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
)

// TestMistAdminCanAdminNode covers the ownership predicate that both
// walls (the gateway resolver and Commodore) must agree on. Mist admin is
// machine-level access, so deny cases are the critical assertions.
func TestMistAdminCanAdminNode(t *testing.T) {
	type c struct {
		name          string
		ownerTenantID string
		callerTenant  string
		callerRole    string
		want          bool
	}
	systemTenant := tenants.SystemTenantID.String()
	cases := []c{
		{"owner-tenant-owner", "tenant-acme", "tenant-acme", "owner", true},
		{"owner-tenant-admin", "tenant-acme", "tenant-acme", "admin", true},
		{"owner-tenant-member-denied", "tenant-acme", "tenant-acme", "member", false},
		{"subscribed-customer-denied", systemTenant, "tenant-customer", "owner", false},
		{"system-owner-break-glass", "", systemTenant, "owner", true},
		{"system-admin-break-glass", "tenant-acme", systemTenant, "admin", true},
		{"system-member-denied", "tenant-acme", systemTenant, "member", false},
		{"different-tenant-denied", "tenant-acme", "tenant-evil", "owner", false},
		{"missing-owner-non-system-denied", "", "tenant-acme", "owner", false},
		{"missing-caller-tenant-denied", "tenant-acme", "", "owner", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mistAdminCanAdminNode(tc.ownerTenantID, tc.callerTenant, tc.callerRole)
			if got != tc.want {
				t.Errorf("mistAdminCanAdminNode(owner=%q, caller=%q, role=%q) = %v; want %v",
					tc.ownerTenantID, tc.callerTenant, tc.callerRole, got, tc.want)
			}
		})
	}
}
