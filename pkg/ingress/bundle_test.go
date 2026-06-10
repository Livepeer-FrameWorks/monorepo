package ingress

import "testing"

func TestIsValidBundleID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"wildcard-frameworks-network", true},
		{"apex-frameworks-network", true},
		{"wildcard-core-central-primary-frameworks-network", true},
		{"a", true},
		{"0", true},
		{"a0-9b", true},

		{"", false},
		{"-leading-hyphen", false},
		{"UPPER", false},
		{"has space", false},
		{"has/slash", false},
		{"has..dotdot", false},
		{"has.dot", false},
		{"../etc/passwd", false},
		{"foo/bar", false},
		{"foo\x00bar", false},
		{"foo;bar", false},
	}
	for _, tc := range cases {
		if got := IsValidBundleID(tc.id); got != tc.want {
			t.Errorf("IsValidBundleID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestTLSPaths(t *testing.T) {
	if got := TLSCertPath("wildcard-frameworks-network"); got != "/etc/frameworks/ingress/tls/wildcard-frameworks-network/tls.crt" {
		t.Errorf("TLSCertPath = %q", got)
	}
	if got := TLSKeyPath("apex-frameworks-network"); got != "/etc/frameworks/ingress/tls/apex-frameworks-network/tls.key" {
		t.Errorf("TLSKeyPath = %q", got)
	}
}

func TestTenantAliasPaths(t *testing.T) {
	if got := TenantAliasCertPath("acme"); got != "/etc/frameworks/ingress/tls/tenant-alias/acme/tls.crt" {
		t.Errorf("TenantAliasCertPath = %q", got)
	}
	if got := TenantAliasKeyPath("acme"); got != "/etc/frameworks/ingress/tls/tenant-alias/acme/tls.key" {
		t.Errorf("TenantAliasKeyPath = %q", got)
	}
	// The directory name itself passes the charset validator — reserving it
	// as a bundle ID is enforced at manifest validation (cluster_provision),
	// not here. This pins that assumption so a validator change is noticed.
	if !IsValidBundleID(TenantAliasDirName) {
		t.Errorf("TenantAliasDirName %q no longer matches the bundle charset; revisit the reservation in cluster_provision", TenantAliasDirName)
	}
}
