package geo

import (
	pb "frameworks/pkg/proto"
	"github.com/uber/h3-go/v4"
)

const (
	defaultResolution = 5
)

// Bucket returns an H3 bucket for the provided lat/lon plus the bucket centroid in degrees.
// If lat/lon are zero it returns nil and false.
func Bucket(lat, lon float64) (*pb.GeoBucket, float64, float64, bool) {
	if lat == 0 && lon == 0 {
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
