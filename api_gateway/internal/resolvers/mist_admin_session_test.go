package resolvers

import (
	"context"
	"testing"
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
		platformOp    bool
		want          bool
	}
	cases := []c{
		{"owner-tenant-owner", "tenant-acme", "tenant-acme", "owner", false, true},
		{"owner-tenant-admin", "tenant-acme", "tenant-acme", "admin", false, true},
		{"owner-tenant-member-denied", "tenant-acme", "tenant-acme", "member", false, false},
		{"subscribed-customer-denied", "tenant-acme", "tenant-customer", "owner", false, false},
		{"platform-operator-break-glass", "", "tenant-x", "member", true, true},
		{"platform-operator-other-tenant", "tenant-acme", "tenant-x", "member", true, true},
		{"different-tenant-denied", "tenant-acme", "tenant-evil", "owner", false, false},
		{"missing-owner-non-operator-denied", "", "tenant-acme", "owner", false, false},
		{"missing-caller-tenant-denied", "tenant-acme", "", "owner", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mistAdminCanAdminNode(context.Background(), tc.ownerTenantID, tc.callerTenant, tc.callerRole, tc.platformOp)
			if got != tc.want {
				t.Errorf("mistAdminCanAdminNode(owner=%q, caller=%q, role=%q, op=%v) = %v; want %v",
					tc.ownerTenantID, tc.callerTenant, tc.callerRole, tc.platformOp, got, tc.want)
			}
		})
	}
}
