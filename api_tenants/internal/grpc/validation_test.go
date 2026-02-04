package grpc

import (
	"database/sql"
	"testing"
)

func TestValidateExpectedIP(t *testing.T) {
	tests := []struct {
		name       string
		expectedIP sql.NullString
		clientIP   string
		want       bool
	}{
		{
			name:       "null expected IP allows any",
			expectedIP: sql.NullString{Valid: false},
			clientIP:   "not-an-ip",
			want:       true,
		},
		{
			name:       "empty expected IP allows any",
			expectedIP: sql.NullString{Valid: true, String: ""},
			clientIP:   "not-an-ip",
			want:       true,
		},
		{
			name:       "exact IPv4 match",
			expectedIP: sql.NullString{Valid: true, String: "10.0.0.1"},
			clientIP:   "10.0.0.1",
			want:       true,
		},
		{
			name:       "exact IPv4 mismatch",
			expectedIP: sql.NullString{Valid: true, String: "10.0.0.1"},
			clientIP:   "10.0.0.2",
			want:       false,
		},
		{
			name:       "CIDR range contains client IP",
			expectedIP: sql.NullString{Valid: true, String: "192.168.1.0/24"},
			clientIP:   "192.168.1.50",
			want:       true,
		},
		{
			name:       "CIDR range does not contain client IP",
			expectedIP: sql.NullString{Valid: true, String: "192.168.1.0/24"},
			clientIP:   "192.168.2.5",
			want:       false,
		},
		{
			name:       "invalid CIDR format",
			expectedIP: sql.NullString{Valid: true, String: "192.168.1.0/33"},
			clientIP:   "192.168.1.50",
			want:       false,
		},
		{
			name:       "invalid client IP format",
			expectedIP: sql.NullString{Valid: true, String: "192.168.1.0/24"},
			clientIP:   "not-an-ip",
			want:       false,
		},
		{
			name:       "invalid expected IP format",
			expectedIP: sql.NullString{Valid: true, String: "not-an-ip"},
			clientIP:   "192.168.1.1",
			want:       false,
		},
		{
			name:       "IPv6 exact match",
			expectedIP: sql.NullString{Valid: true, String: "2001:db8::1"},
			clientIP:   "2001:db8::1",
			want:       true,
		},
		{
			name:       "IPv6 CIDR match",
			expectedIP: sql.NullString{Valid: true, String: "2001:db8::/32"},
			clientIP:   "2001:db8::1",
			want:       true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := validateExpectedIP(tc.expectedIP, tc.clientIP); got != tc.want {
				t.Fatalf("validateExpectedIP(%+v, %q) = %v, want %v", tc.expectedIP, tc.clientIP, got, tc.want)
			}
		})
	}
}
