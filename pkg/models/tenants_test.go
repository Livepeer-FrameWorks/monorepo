package models

import "testing"

// Tenant aliases unlock only on the monthly paid tiers. free AND payg
// (prepaid pay-as-you-go) are ineligible, and unknown/empty values fail
// closed — adding a new monthly tier requires touching the allowlist.
func TestDeploymentTierAliasEligible(t *testing.T) {
	cases := []struct {
		tier string
		want bool
	}{
		{"supporter", true},
		{"developer", true},
		{"production", true},
		{"enterprise", true},
		{"  Supporter ", true}, // trimmed + case-insensitive
		{"free", false},
		{"payg", false},
		{"", false},
		{"global", false},  // legacy bootstrap stamp
		{"pro", false},     // unknown names fail closed
		{"starter", false}, // not a real tier
	}
	for _, tc := range cases {
		if got := DeploymentTierAliasEligible(tc.tier); got != tc.want {
			t.Errorf("DeploymentTierAliasEligible(%q) = %v, want %v", tc.tier, got, tc.want)
		}
	}
}
