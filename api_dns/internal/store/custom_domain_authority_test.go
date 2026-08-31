package store

import "testing"

func TestCustomDomainCredentialAndBundleAuthoritySets(t *testing.T) {
	tests := []struct {
		status          string
		retains         bool
		joinsTenantSANs bool
	}{
		{status: "pending_verification"},
		{status: "verified", retains: true, joinsTenantSANs: true},
		{status: "cert_issuing", retains: true, joinsTenantSANs: true},
		{status: "cert_issued", retains: true, joinsTenantSANs: true},
		{status: "cert_failed", retains: true},
		{status: "tearing_down"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			if got := CustomDomainHasCertificateAuthority(test.status); got != test.retains {
				t.Fatalf("credential retention=%v, want %v", got, test.retains)
			}
			if got := CustomDomainParticipatesInTenantBundle(test.status); got != test.joinsTenantSANs {
				t.Fatalf("tenant SAN participation=%v, want %v", got, test.joinsTenantSANs)
			}
		})
	}
}
