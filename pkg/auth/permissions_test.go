package auth

import "testing"

func TestPermissionGranted(t *testing.T) {
	cases := []struct {
		granted  []string
		required string
		want     bool
	}{
		{[]string{"streams:read"}, "streams:read", true},
		{[]string{"streams:write"}, "streams:read", true},
		{[]string{" streams:write "}, "streams:read", true},
		{[]string{"streams:read"}, "streams:write", false},
		{[]string{"analytics:read"}, "streams:read", false},
		{[]string{"billing:write"}, "streams:read", false},
		{nil, "streams:read", false},
		{nil, "", true},
	}
	for _, tc := range cases {
		if got := PermissionGranted(tc.granted, tc.required); got != tc.want {
			t.Errorf("PermissionGranted(%v, %q) = %v, want %v", tc.granted, tc.required, got, tc.want)
		}
	}
}
