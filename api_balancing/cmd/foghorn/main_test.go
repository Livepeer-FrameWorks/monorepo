package main

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
)

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

func TestFoghornRelayAdvertiseAddr(t *testing.T) {
	t.Run("explicit addr wins", func(t *testing.T) {
		t.Setenv("FOGHORN_RELAY_ADVERTISE_ADDR", "regional-eu-1.internal:18019")
		t.Setenv("FOGHORN_RELAY_ADVERTISE_HOST", "ignored.internal")

		got := foghornRelayAdvertiseAddr(":19000", "public.example:18029")
		if got != "regional-eu-1.internal:18019" {
			t.Fatalf("relay addr=%q, want explicit", got)
		}
	})

	t.Run("host override uses internal bind port", func(t *testing.T) {
		t.Setenv("FOGHORN_RELAY_ADVERTISE_HOST", "regional-eu-2.internal")

		got := foghornRelayAdvertiseAddr("0.0.0.0:18019", "foghorn.media-eu-1.example:18029")
		if got != "regional-eu-2.internal:18019" {
			t.Fatalf("relay addr=%q", got)
		}
	})

	t.Run("fallback host is only a last resort", func(t *testing.T) {
		got := foghornRelayAdvertiseAddr(":18019", "foghorn.media-eu-1.example:18029")
		if got != "foghorn.media-eu-1.example:18019" {
			t.Fatalf("relay addr=%q", got)
		}
	})

	t.Run("production rejects loopback fallback", func(t *testing.T) {
		t.Setenv("BUILD_ENV", "production")

		got := foghornRelayAdvertiseAddr(":18019", "")
		if got != "" {
			t.Fatalf("relay addr=%q, want empty without production-safe advertise host", got)
		}
	})
}

func TestRelayHealthResult(t *testing.T) {
	tests := []struct {
		name       string
		ready      bool
		required   bool
		wantStatus string
	}{
		{name: "ready", ready: true, required: true, wantStatus: monitoring.StatusHealthy},
		{name: "optional missing", ready: false, required: false, wantStatus: monitoring.StatusDegraded},
		{name: "required missing", ready: false, required: true, wantStatus: monitoring.StatusUnhealthy},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := relayHealthResult(tc.ready, tc.required)
			if got.Status != tc.wantStatus {
				t.Fatalf("relayHealthResult() status=%q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}
