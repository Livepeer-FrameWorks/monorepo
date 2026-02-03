package geo

import (
	"math"

	pb "frameworks/pkg/proto"
	"github.com/uber/h3-go/v4"
)

const (
	defaultResolution = 5
)

// Bucket returns an H3 bucket for the provided lat/lon plus the bucket centroid in degrees.
// Returns false when coordinates are invalid.
func Bucket(lat, lon float64) (*pb.GeoBucket, float64, float64, bool) {
	if !IsValidLatLon(lat, lon) {
		return nil, 0, 0, false
	}

	latLng := h3.NewLatLng(lat, lon)
	cell := h3.LatLngToCell(latLng, defaultResolution)

	if cell == 0 {
		return nil, 0, 0, false
	}

	centroid := h3.CellToLatLng(cell)
	return &pb.GeoBucket{
			H3Index:    uint64(cell),
			Resolution: defaultResolution,
		},
		centroid.Lat,
		centroid.Lng,
		true
}

// IsValidLatLon validates geographic coordinates.
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
	return true
}
