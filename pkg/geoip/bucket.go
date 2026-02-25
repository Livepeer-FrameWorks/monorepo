package geoip

import "math"

// DefaultResolution is H3 resolution 5 (~252 km² hexagons).
// This prevents exposing exact server locations while still being
// useful for geographic routing decisions.
const DefaultResolution = 5

// IsValidLatLon validates geographic coordinates.
// Rejects NaN, Inf, out-of-range, and 0,0 (common default value, located in the ocean).
func IsValidLatLon(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return false
	}
	if lat < -90 || lat > 90 {
		return false
	}
	if lon < -180 || lon > 180 {
		return false
	}
	if lat == 0 && lon == 0 {
		return false
	}
	return true
}
