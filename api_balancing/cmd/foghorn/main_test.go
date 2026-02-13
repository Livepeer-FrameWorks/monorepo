package main

import "testing"

func TestControlPortFromBindAddr(t *testing.T) {
	tests := []struct {
		name     string
		bindAddr string
		fallback int
		want     int
	}{
		{name: "host and port", bindAddr: "0.0.0.0:18029", fallback: 18019, want: 18029},
		{name: "port only", bindAddr: ":19000", fallback: 18019, want: 19000},
		{name: "invalid", bindAddr: "invalid", fallback: 18019, want: 18019},
		{name: "invalid port", bindAddr: "127.0.0.1:not-a-port", fallback: 18019, want: 18019},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := controlPortFromBindAddr(tc.bindAddr, tc.fallback)
			if got != tc.want {
				t.Fatalf("controlPortFromBindAddr(%q)=%d, want %d", tc.bindAddr, got, tc.want)
			}
		})
	}
}
