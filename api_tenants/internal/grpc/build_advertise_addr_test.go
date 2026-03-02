package grpc

import (
	"database/sql"
	"testing"
)

func TestBuildAdvertiseAddr(t *testing.T) {
	tests := []struct {
		name     string
		host     sql.NullString
		port     sql.NullInt32
		wantAddr string
		wantOK   bool
	}{
		{
			name:     "ipv4 host and valid port",
			host:     sql.NullString{String: "10.0.0.1", Valid: true},
			port:     sql.NullInt32{Int32: 18000, Valid: true},
			wantAddr: "10.0.0.1:18000",
			wantOK:   true,
		},
		{
			name:     "ipv6 host gets bracketed",
			host:     sql.NullString{String: "::1", Valid: true},
			port:     sql.NullInt32{Int32: 18000, Valid: true},
			wantAddr: "[::1]:18000",
			wantOK:   true,
		},
		{
			name:     "ipv6 with existing brackets stripped and re-applied",
			host:     sql.NullString{String: "[::1]", Valid: true},
			port:     sql.NullInt32{Int32: 18000, Valid: true},
			wantAddr: "[::1]:18000",
			wantOK:   true,
		},
		{
			name:     "hostname host",
			host:     sql.NullString{String: "bridge.mesh", Valid: true},
			port:     sql.NullInt32{Int32: 18001, Valid: true},
			wantAddr: "bridge.mesh:18001",
			wantOK:   true,
		},
		{
			name:   "null host returns false",
			host:   sql.NullString{Valid: false},
			port:   sql.NullInt32{Int32: 18000, Valid: true},
			wantOK: false,
		},
		{
			name:   "null port returns false",
			host:   sql.NullString{String: "10.0.0.1", Valid: true},
			port:   sql.NullInt32{Valid: false},
			wantOK: false,
		},
		{
			name:   "empty host returns false",
			host:   sql.NullString{String: "", Valid: true},
			port:   sql.NullInt32{Int32: 18000, Valid: true},
			wantOK: false,
		},
		{
			name:   "whitespace-only host returns false",
			host:   sql.NullString{String: "   ", Valid: true},
			port:   sql.NullInt32{Int32: 18000, Valid: true},
			wantOK: false,
		},
		{
			name:   "port zero returns false",
			host:   sql.NullString{String: "10.0.0.1", Valid: true},
			port:   sql.NullInt32{Int32: 0, Valid: true},
			wantOK: false,
		},
		{
			name:   "port exceeding 65535 returns false",
			host:   sql.NullString{String: "10.0.0.1", Valid: true},
			port:   sql.NullInt32{Int32: 70000, Valid: true},
			wantOK: false,
		},
		{
			name:   "negative port returns false",
			host:   sql.NullString{String: "10.0.0.1", Valid: true},
			port:   sql.NullInt32{Int32: -1, Valid: true},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, ok := buildAdvertiseAddr(tt.host, tt.port)
			if ok != tt.wantOK {
				t.Fatalf("expected ok=%v, got %v", tt.wantOK, ok)
			}
			if ok && addr != tt.wantAddr {
				t.Fatalf("expected addr=%q, got %q", tt.wantAddr, addr)
			}
		})
	}
}
