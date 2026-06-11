package auth

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
)

func TestIsPlatformOperator(t *testing.T) {
	system := tenants.SystemTenantID.String()
	cases := []struct {
		name     string
		tenantID string
		role     string
		want     bool
	}{
		{"system owner", system, "owner", true},
		{"system admin", system, "admin", true},
		{"system admin mixed case + whitespace", " " + system + " ", " Admin ", true},
		{"system member", system, "member", false},
		{"system empty role", system, "", false},
		{"other tenant owner", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "owner", false},
		{"empty tenant", "", "owner", false},
		{"anonymous tenant", tenants.AnonymousTenantID.String(), "admin", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPlatformOperator(tc.tenantID, tc.role); got != tc.want {
				t.Fatalf("IsPlatformOperator(%q, %q) = %v, want %v", tc.tenantID, tc.role, got, tc.want)
			}
		})
	}
}
