package bootstrap

import "testing"

func TestSameHostIPTreatsHostPrefixAsSameAddress(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"91.99.236.223/32", "91.99.236.223", true},
		{"2a01:7c8:aaca:2ec::1/128", "2a01:7c8:aaca:2ec::1", true},
		{"10.88.156.88/32", "10.88.156.88/32", true},
		{"91.99.236.223/31", "91.99.236.223", false},
		{"91.99.236.223", "91.99.236.224", false},
		{"not-an-ip", "not-an-ip", true},
		{"not-an-ip", "91.99.236.223", false},
	}
	for _, tc := range cases {
		if got := sameHostIP(tc.a, tc.b); got != tc.want {
			t.Fatalf("sameHostIP(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
