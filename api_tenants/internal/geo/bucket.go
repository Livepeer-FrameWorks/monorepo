package geo

import (
	"frameworks/pkg/geoip"

	"github.com/uber/h3-go/v4"
)

// BucketGeoData replaces the Latitude/Longitude on a GeoData with the
// centroid of its H3 cell at DefaultResolution. No-op when nil or invalid.
func BucketGeoData(g *geoip.GeoData) {
	if g == nil || !geoip.IsValidLatLon(g.Latitude, g.Longitude) {
		return
	}

	latLng := h3.NewLatLng(g.Latitude, g.Longitude)
	cell, err := h3.LatLngToCell(latLng, geoip.DefaultResolution)
	if err != nil || cell == 0 {
		return
	}

	centroid, err := h3.CellToLatLng(cell)
	if err != nil {
		return
	}
	g.Latitude = centroid.Lat
	g.Longitude = centroid.Lng
}
