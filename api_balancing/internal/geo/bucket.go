package geo

import (
	pb "frameworks/pkg/proto"

	"frameworks/pkg/geoip"

	"github.com/uber/h3-go/v4"
)

// BucketResult contains the H3-bucketed coordinates for a location.
type BucketResult struct {
	H3Index    uint64
	Resolution int
	Latitude   float64
	Longitude  float64
}

// BucketCoords snaps lat/lon to the centroid of an H3 cell at the given resolution.
func BucketCoords(lat, lon float64, resolution int) *BucketResult {
	if !geoip.IsValidLatLon(lat, lon) {
		return nil
	}

	latLng := h3.NewLatLng(lat, lon)
	cell, err := h3.LatLngToCell(latLng, resolution)
	if err != nil || cell == 0 {
		return nil
	}

	centroid, err := h3.CellToLatLng(cell)
	if err != nil {
		return nil
	}
	return &BucketResult{
		H3Index:    uint64(cell),
		Resolution: resolution,
		Latitude:   centroid.Lat,
		Longitude:  centroid.Lng,
	}
}

// Bucket returns an H3 bucket for the provided lat/lon plus the bucket centroid in degrees.
// Returns false when coordinates are invalid.
func Bucket(lat, lon float64) (*pb.GeoBucket, float64, float64, bool) {
	b := BucketCoords(lat, lon, geoip.DefaultResolution)
	if b == nil {
		return nil, 0, 0, false
	}
	return &pb.GeoBucket{
		H3Index:    b.H3Index,
		Resolution: uint32(b.Resolution),
	}, b.Latitude, b.Longitude, true
}

// IsValidLatLon validates geographic coordinates.
func IsValidLatLon(lat, lon float64) bool {
	return geoip.IsValidLatLon(lat, lon)
}

// BucketGeoData replaces the Latitude/Longitude on a GeoData with bucketed centroids.
func BucketGeoData(g *geoip.GeoData) {
	if g == nil {
		return
	}
	b := BucketCoords(g.Latitude, g.Longitude, geoip.DefaultResolution)
	if b == nil {
		return
	}
	g.Latitude = b.Latitude
	g.Longitude = b.Longitude
}
