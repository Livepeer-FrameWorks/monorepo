package geoip

import (
	"math"
	"testing"
)

// IsValidLatLon is the front-line guard for every geo-derived routing
// decision: anything it rejects is treated as "no location". The cases that
// matter are the ones that are numerically in range but semantically junk —
// chiefly the 0,0 null-island default that MMDB and client payloads emit when
// they have no real fix.
func TestIsValidLatLon(t *testing.T) {
	tests := []struct {
		name     string
		lat, lon float64
		want     bool
	}{
		{"valid mid-latitude", 52.37, 4.90, true},
		{"valid southern hemisphere", -33.87, 151.21, true},
		{"north pole edge", 90, 0.0001, true},
		{"south pole edge", -90, -179.999, true},
		{"antimeridian east edge", 12.34, 180, true},
		{"antimeridian west edge", 12.34, -180, true},

		// The load-bearing rejection: 0,0 is the "no fix" default, not a real
		// location in the Gulf of Guinea.
		{"null island rejected", 0, 0, false},
		// A real point on the equator or prime meridian must still pass — the
		// guard rejects only the exact 0,0 pair, not either axis alone.
		{"equator non-zero lon valid", 0, 4.90, true},
		{"prime meridian non-zero lat valid", 52.37, 0, true},

		{"lat above range", 90.0001, 0, false},
		{"lat below range", -90.0001, 0, false},
		{"lon above range", 0, 180.0001, false},
		{"lon below range", 0, -180.0001, false},

		{"NaN lat", math.NaN(), 4.90, false},
		{"NaN lon", 52.37, math.NaN(), false},
		{"+Inf lat", math.Inf(1), 4.90, false},
		{"-Inf lon", 52.37, math.Inf(-1), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidLatLon(tt.lat, tt.lon); got != tt.want {
				t.Errorf("IsValidLatLon(%v, %v) = %v, want %v", tt.lat, tt.lon, got, tt.want)
			}
		})
	}
}
